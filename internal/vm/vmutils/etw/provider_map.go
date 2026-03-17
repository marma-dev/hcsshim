package etw

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/Microsoft/go-winio/pkg/guid"
	"github.com/Microsoft/hcsshim/internal/log"
)

// Log Sources JSON structure
type LogSourcesInfo struct {
	LogConfig LogConfig `json:"LogConfig"`
}

type LogConfig struct {
	Sources []Source `json:"sources"`
}

type Source struct {
	Type      string        `json:"type"`
	Providers []EtwProvider `json:"providers"`
}

type EtwProvider struct {
	ProviderName string `json:"providerName,omitempty"`
	ProviderGUID string `json:"providerGuid,omitempty"`
	Level        string `json:"level,omitempty"`
	Keywords     string `json:"keywords,omitempty"`
}

// GetDefaultLogSources returns the default log sources configuration.
func GetDefaultLogSources() LogSourcesInfo {
	return defaultLogSourcesInfo
}

// GetProviderGUIDFromName returns the provider GUID for a given provider name. If the provider name is not found in the map, it returns an empty string.
func getProviderGUIDFromName(providerName string) string {
	if guid, ok := etwNameToGUIDMap[strings.ToLower(providerName)]; ok {
		return guid
	}
	return ""
}

// providerKey returns a unique key for an EtwProvider, used for deduplication during merge.
// If both Name and GUID are present, key is "Name|GUID". If only GUID, key is GUID. Otherwise, key is Name.
func providerKey(provider EtwProvider) string {
	if provider.ProviderGUID != "" {
		if provider.ProviderName != "" {
			return provider.ProviderName + "|" + provider.ProviderGUID
		}
		return provider.ProviderGUID
	}
	return provider.ProviderName
}

// mergeProviders merges two slices of EtwProvider, with userProviders taking precedence over defaultProviders
// on key conflicts (same name, same GUID, or same name|GUID combination).
func mergeProviders(defaultProviders, userProviders []EtwProvider) []EtwProvider {
	providerMap := make(map[string]EtwProvider)
	for _, provider := range defaultProviders {
		providerMap[providerKey(provider)] = provider
	}
	for _, provider := range userProviders {
		providerMap[providerKey(provider)] = provider
	}

	merged := make([]EtwProvider, 0, len(providerMap))
	for _, provider := range providerMap {
		merged = append(merged, provider)
	}
	return merged
}

// mergeLogSources merges userSources into resultSources. Sources with matching types have their
// providers merged; unmatched user sources are appended as new entries.
func mergeLogSources(resultSources []Source, userSources []Source) []Source {
	for _, userSrc := range userSources {
		merged := false
		for i, resSrc := range resultSources {
			if userSrc.Type == resSrc.Type {
				resultSources[i].Providers = mergeProviders(resSrc.Providers, userSrc.Providers)
				merged = true
				break
			}
		}
		if !merged {
			resultSources = append(resultSources, userSrc)
		}
	}
	return resultSources
}

// decodeAndUnmarshalLogSources decodes a base64-encoded JSON string and unmarshals it into a LogSourcesInfo.
func decodeAndUnmarshalLogSources(ctx context.Context, base64EncodedJSONLogConfig string) (LogSourcesInfo, error) {
	jsonBytes, err := base64.StdEncoding.DecodeString(base64EncodedJSONLogConfig)
	if err != nil {
		log.G(ctx).Errorf("Error decoding base64 log config: %v", err)
		return LogSourcesInfo{}, err
	}

	var userLogSources LogSourcesInfo
	if err := json.Unmarshal(jsonBytes, &userLogSources); err != nil {
		log.G(ctx).Errorf("Error unmarshalling user log config: %v", err)
		return LogSourcesInfo{}, err
	}
	return userLogSources, nil
}

func trimGUID(in string) string {
	s := strings.TrimSpace(in)
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	s = strings.TrimSpace(s)
	return s
}

// resolveGUIDsWithLookup normalizes and fills in provider GUIDs from the well-known ETW map
// for all providers across all sources. Providers with an invalid GUID are warned and skipped.
func resolveGUIDsWithLookup(ctx context.Context, sources []Source) []Source {
	for i, src := range sources {
		for j, provider := range src.Providers {
			if provider.ProviderGUID != "" {
				guid, err := guid.FromString(trimGUID(provider.ProviderGUID))
				if err != nil {
					log.G(ctx).Warningf("Skipping invalid GUID %q for provider %q: %v", provider.ProviderGUID, provider.ProviderName, err)
					continue
				}
				sources[i].Providers[j].ProviderGUID = strings.ToLower(guid.String())
			}
			if provider.ProviderName != "" && provider.ProviderGUID == "" {
				sources[i].Providers[j].ProviderGUID = getProviderGUIDFromName(provider.ProviderName)
			}
		}
	}
	return sources
}

// stripRedundantGUIDs removes the GUID from providers where both Name and GUID are present and
// the GUID matches the well-known lookup by name. This ensures sidecar-GCS prefers name-based
// policy verification. Invalid GUIDs are warned and left as-is after normalization.
func stripRedundantGUIDs(ctx context.Context, sources []Source) []Source {
	for i, src := range sources {
		for j, provider := range src.Providers {
			if provider.ProviderName == "" || provider.ProviderGUID == "" {
				continue
			}
			guid, err := guid.FromString(trimGUID(provider.ProviderGUID))
			if err != nil {
				log.G(ctx).Warningf("Skipping invalid GUID %q for provider %q: %v", provider.ProviderGUID, provider.ProviderName, err)
				continue
			}
			if strings.EqualFold(guid.String(), getProviderGUIDFromName(provider.ProviderName)) {
				sources[i].Providers[j].ProviderGUID = ""
			} else {
				sources[i].Providers[j].ProviderGUID = strings.ToLower(guid.String())
			}
		}
	}
	return sources
}

// applyGUIDPolicy applies GUID resolution or stripping to all sources depending on the includeGUIDs flag.
// See resolveGUIDsWithLookup and stripRedundantGUIDs for the respective behaviors.
func applyGUIDPolicy(ctx context.Context, sources []Source, includeGUIDs bool) []Source {
	if len(sources) == 0 {
		return sources
	}
	if includeGUIDs {
		return resolveGUIDsWithLookup(ctx, sources)
	}
	return stripRedundantGUIDs(ctx, sources)
}

// marshalAndEncodeLogSources marshals the given LogSourcesInfo to JSON and encodes it as a base64 string.
// On error, it logs and returns the original fallback string.
func marshalAndEncodeLogSources(ctx context.Context, logCfg LogSourcesInfo, fallback string) (string, error) {
	jsonBytes, err := json.Marshal(logCfg)
	if err != nil {
		log.G(ctx).Errorf("Error marshalling log config: %v", err)
		return fallback, err
	}
	return base64.StdEncoding.EncodeToString(jsonBytes), nil
}

// UpdateLogSources updates the user provided log sources with the default log sources based on the
// configuration and returns the updated log sources as a base64 encoded JSON string.
// If there is an error in the process, it returns the original user provided log sources string.
func UpdateLogSources(ctx context.Context, base64EncodedJSONLogConfig string, useDefaultLogSources bool, includeGUIDs bool) string {
	var resultLogCfg LogSourcesInfo
	if useDefaultLogSources {
		resultLogCfg = defaultLogSourcesInfo
	}

	if base64EncodedJSONLogConfig != "" {
		userLogSources, err := decodeAndUnmarshalLogSources(ctx, base64EncodedJSONLogConfig)
		if err == nil {
			resultLogCfg.LogConfig.Sources = mergeLogSources(resultLogCfg.LogConfig.Sources, userLogSources.LogConfig.Sources)
		}
	}

	resultLogCfg.LogConfig.Sources = applyGUIDPolicy(ctx, resultLogCfg.LogConfig.Sources, includeGUIDs)

	result, err := marshalAndEncodeLogSources(ctx, resultLogCfg, base64EncodedJSONLogConfig)
	if err != nil {
		return base64EncodedJSONLogConfig
	}
	return result
}
