package main

import "hayami/internal/config"

// Config and Provider are aliases so the UI code keeps using the local names
// while the model, defaults, validation and save logic live in
// internal/config.
type Config = config.Config
type Provider = config.Provider

func defaultConfig() Config { return config.Default() }

func configPath() (string, error) { return config.Path() }

func userDataDir() string { return config.UserDataDir() }

func loadConfig() (Config, error) { return config.Load() }

func decodeConfig(data []byte) (Config, error) { return config.Decode(data) }

func validateConfig(c Config) error { return config.Validate(c) }

func writeConfigSchema() error { return config.WriteSchema() }
