package app_test

import "github.com/razzie-cloud/notes/internal/config"

func structConfig() config.Config {
	return config.Config{Origin: "http://notes.localhost:8080"}
}
