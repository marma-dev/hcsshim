package etw

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Microsoft/go-winio/pkg/guid"
)

func TestGetProviderGUIDFromName(t *testing.T) {
	// These names should be present in the etwNameToGUIDMap for the tests to pass.
	tests := []struct {
		name     string
		expected string
	}{
		{"Microsoft.Windows.HyperV.Compute", etwNameToGUIDMap["microsoft.windows.hyperv.compute"]},
		{"Microsoft.Windows.Containers.Setup", etwNameToGUIDMap["microsoft.windows.containers.setup"]},
		{"nonexistent.provider", ""},
		{"", ""},
	}

	for _, tt := range tests {
		got := getProviderGUIDFromName(tt.name)
		if got != tt.expected {
			t.Errorf("getProviderGUIDFromName(%q) = %q, want %q", tt.name, got, tt.expected)
		}
	}
}

func TestUpdateLogSources_Combinations(t *testing.T) {
	originalDefaults := cloneLogSourcesInfo(defaultLogSourcesInfo)
	t.Cleanup(func() {
		defaultLogSourcesInfo = cloneLogSourcesInfo(originalDefaults)
	})

	userConfig := buildTestUserLogSources(t)

	tests := []struct {
		name           string
		base64Input    string
		useDefault     bool
		includeGUIDs   bool
		expectedLogCfg LogSourcesInfo
	}{
		{
			name:           "empty_input_no_defaults_no_guids",
			base64Input:    "",
			useDefault:     false,
			includeGUIDs:   false,
			expectedLogCfg: LogSourcesInfo{},
		},
		{
			name:           "empty_input_no_defaults_with_guids",
			base64Input:    "",
			useDefault:     false,
			includeGUIDs:   true,
			expectedLogCfg: LogSourcesInfo{},
		},
		{
			name:           "empty_input_with_defaults_no_guids",
			base64Input:    "",
			useDefault:     true,
			includeGUIDs:   false,
			expectedLogCfg: expectedLogSources(originalDefaults, LogSourcesInfo{}, true, false, false),
		},
		{
			name:           "empty_input_with_defaults_with_guids",
			base64Input:    "",
			useDefault:     true,
			includeGUIDs:   true,
			expectedLogCfg: expectedLogSources(originalDefaults, LogSourcesInfo{}, true, true, false),
		},
		{
			name:           "user_input_no_defaults_no_guids",
			base64Input:    mustEncodeLogSources(t, userConfig),
			useDefault:     false,
			includeGUIDs:   false,
			expectedLogCfg: expectedLogSources(originalDefaults, userConfig, false, false, true),
		},
		{
			name:           "user_input_no_defaults_with_guids",
			base64Input:    mustEncodeLogSources(t, userConfig),
			useDefault:     false,
			includeGUIDs:   true,
			expectedLogCfg: expectedLogSources(originalDefaults, userConfig, false, true, true),
		},
		{
			name:           "user_input_with_defaults_no_guids",
			base64Input:    mustEncodeLogSources(t, userConfig),
			useDefault:     true,
			includeGUIDs:   false,
			expectedLogCfg: expectedLogSources(originalDefaults, userConfig, true, false, true),
		},
		{
			name:           "user_input_with_defaults_with_guids",
			base64Input:    mustEncodeLogSources(t, userConfig),
			useDefault:     true,
			includeGUIDs:   true,
			expectedLogCfg: expectedLogSources(originalDefaults, userConfig, true, true, true),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defaultLogSourcesInfo = cloneLogSourcesInfo(originalDefaults)

			gotEncoded := UpdateLogSources(context.Background(), tt.base64Input, tt.useDefault, tt.includeGUIDs)
			got := mustDecodeLogSources(t, gotEncoded)

			if !reflect.DeepEqual(got, tt.expectedLogCfg) {
				t.Fatalf("unexpected log config.\n got: %#v\nwant: %#v", got, tt.expectedLogCfg)
			}
		})
	}
}

func buildTestUserLogSources(t *testing.T) LogSourcesInfo {
	t.Helper()

	nameOnlyProvider := "Microsoft.Windows.HyperV.Compute"
	nameAndGUIDProvider := "Microsoft.Windows.Containers.Setup"

	guid := getProviderGUIDFromName(nameAndGUIDProvider)
	if guid == "" {
		t.Fatalf("missing GUID mapping for provider %q", nameAndGUIDProvider)
	}
	if getProviderGUIDFromName(nameOnlyProvider) == "" {
		t.Fatalf("missing GUID mapping for provider %q", nameOnlyProvider)
	}

	return LogSourcesInfo{
		LogConfig: LogConfig{
			Sources: []Source{
				{
					Type: "UserETW",
					Providers: []EtwProvider{
						{
							ProviderName: nameOnlyProvider,
							Level:        "Verbose",
						},
						{
							ProviderName: nameAndGUIDProvider,
							ProviderGUID: "{" + strings.ToUpper(guid) + "}",
							Level:        "Warning",
						},
					},
				},
			},
		},
	}
}

func expectedLogSources(defaults LogSourcesInfo, user LogSourcesInfo, useDefault bool, includeGUIDs bool, includeUser bool) LogSourcesInfo {
	var result LogSourcesInfo

	if useDefault {
		result = cloneLogSourcesInfo(defaults)
	}

	if includeUser {
		userCopy := cloneLogSourcesInfo(user)
		result.LogConfig.Sources = append(result.LogConfig.Sources, userCopy.LogConfig.Sources...)
	}

	applyExpectedGUIDBehavior(&result, includeGUIDs)
	return result
}

func applyExpectedGUIDBehavior(cfg *LogSourcesInfo, includeGUIDs bool) {
	for i, src := range cfg.LogConfig.Sources {
		for j, provider := range src.Providers {
			if includeGUIDs {
				if provider.ProviderGUID != "" {
					guid, err := guid.FromString(trimGUID(provider.ProviderGUID))
					if err != nil {
						cfg.LogConfig.Sources[i].Providers[j].ProviderGUID = ""
					} else {
						cfg.LogConfig.Sources[i].Providers[j].ProviderGUID = strings.ToLower(guid.String())
					}
				}
				if provider.ProviderName != "" && provider.ProviderGUID == "" {
					cfg.LogConfig.Sources[i].Providers[j].ProviderGUID = getProviderGUIDFromName(provider.ProviderName)
				}
				continue
			}

			if provider.ProviderName != "" && provider.ProviderGUID != "" {
				guid, err := guid.FromString(trimGUID(provider.ProviderGUID))
				if err != nil {
					continue
				}
				if strings.EqualFold(guid.String(), getProviderGUIDFromName(provider.ProviderName)) {
					cfg.LogConfig.Sources[i].Providers[j].ProviderGUID = ""
				} else {
					cfg.LogConfig.Sources[i].Providers[j].ProviderGUID = strings.ToLower(guid.String())
				}
			}
		}
	}
}

func cloneLogSourcesInfo(in LogSourcesInfo) LogSourcesInfo {
	out := LogSourcesInfo{}
	if in.LogConfig.Sources == nil {
		return out
	}

	out.LogConfig.Sources = make([]Source, len(in.LogConfig.Sources))
	for i, src := range in.LogConfig.Sources {
		out.LogConfig.Sources[i].Type = src.Type
		if src.Providers != nil {
			out.LogConfig.Sources[i].Providers = append([]EtwProvider(nil), src.Providers...)
		}
	}
	return out
}

func mustEncodeLogSources(t *testing.T, cfg LogSourcesInfo) string {
	t.Helper()

	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("failed to marshal log sources: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func mustDecodeLogSources(t *testing.T, encoded string) LogSourcesInfo {
	t.Helper()

	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("failed to decode base64 log sources: %v", err)
	}

	var cfg LogSourcesInfo
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("failed to unmarshal log sources: %v", err)
	}
	return cfg
}
