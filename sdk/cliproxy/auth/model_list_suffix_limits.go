package auth

import (
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func (m *Manager) ConservativeSuffixModelLimits(modelID string, initial registry.ModelListLimits) registry.ModelListLimits {
	if m == nil {
		return registry.ModelListLimits{}
	}
	configSnapshot, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if configSnapshot == nil || configSnapshot.Home.Enabled || !payloadRulesPreserveListLimits(configSnapshot.Payload) {
		return registry.ModelListLimits{}
	}
	registryRef := registry.GetGlobalRegistry()
	registryEpoch := registryRef.RegistrationEpoch()
	authEpoch := m.structuralEpoch.Load()
	aliasSnapshot := m.oauthModelAlias.Load()
	routingSnapshot := m.apiKeyModelRouting.Load()
	eligible := false
	limits := initial
	for _, credential := range m.snapshotAuths() {
		if credential == nil || !m.authSupportsRouteModel(registryRef, credential, modelID) {
			continue
		}
		eligible = true
		if credential.AuthKind() != AuthKindOAuth || isConfiguredModelRoutingAuth(credential) ||
			strings.TrimSpace(credential.Attributes[homeUpstreamModelAttributeKey]) != "" {
			return registry.ModelListLimits{}
		}
		target := canonicalModelKey(m.selectionModelForAuth(credential, modelID))
		found := false
		for _, info := range registryRef.GetModelsForClient(credential.ID) {
			if info == nil {
				continue
			}
			metadataID := strings.TrimSpace(info.MetadataModelID)
			if metadataID == "" {
				metadataID = info.ID
			}
			if !strings.EqualFold(canonicalModelKey(metadataID), target) {
				continue
			}
			found = true
			declared := registry.DeclaredModelListLimits(info)
			limits.ContextLength = minimumKnownListLimit(limits.ContextLength, declared.ContextLength)
			limits.MaxCompletionTokens = minimumKnownListLimit(limits.MaxCompletionTokens, declared.MaxCompletionTokens)
		}
		if !found {
			return registry.ModelListLimits{}
		}
	}
	if !eligible || registryRef.RegistrationEpoch() != registryEpoch || m.structuralEpoch.Load() != authEpoch ||
		m.oauthModelAlias.Load() != aliasSnapshot || m.apiKeyModelRouting.Load() != routingSnapshot ||
		m.runtimeConfig.Load() != configSnapshot {
		return registry.ModelListLimits{}
	}
	return registry.DeclaredModelListLimits(&registry.ModelInfo{ListLimits: &limits})
}

func payloadRulesPreserveListLimits(payload internalconfig.PayloadConfig) bool {
	for _, rules := range [][]internalconfig.PayloadRule{payload.Default, payload.DefaultRaw, payload.Override, payload.OverrideRaw} {
		for _, rule := range rules {
			for path := range rule.Params {
				if !listSafePayloadPath(path) {
					return false
				}
			}
		}
	}
	for _, rule := range payload.Filter {
		for _, path := range rule.Params {
			if !listSafePayloadPath(path) {
				return false
			}
		}
	}
	return true
}

func listSafePayloadPath(path string) bool {
	switch strings.TrimSpace(path) {
	case "service_tier", "reasoning.effort":
		return true
	default:
		return false
	}
}

func minimumKnownListLimit(first, second int) int {
	if first <= 0 || second <= 0 {
		return 0
	}
	return min(first, second)
}
