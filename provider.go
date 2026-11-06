package main

import "hayami/internal/config"

const (
	providerOpenAI = config.ProviderOpenAI
	providerAzure  = config.ProviderAzure
	providerBaidu  = config.ProviderBaidu

	defaultAzureEndpoint = config.DefaultAzureEndpoint
	defaultBaiduEndpoint = config.DefaultBaiduEndpoint
)

func providerKindName(kind string) string { return config.KindName(kind) }

func defaultProviderName(kind string) string { return config.DefaultProviderName(kind) }

func providerSecretKey(p Provider) string { return config.SecretKey(p) }

func providerConfigured(p Provider, key string) bool { return config.Configured(p, key) }

func providerLabel(p Provider) string { return config.Label(p) }

func activeProviderLabel(cfg Config) string { return config.ActiveLabel(cfg) }

func hasConfiguredProvider(cfg Config) bool { return config.HasConfigured(cfg) }

func nextProviderID(cfg Config, kind string) string { return config.NextProviderID(cfg, kind) }
