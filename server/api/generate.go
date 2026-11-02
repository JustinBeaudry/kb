// Package api owns the HTTP contract and reproducible code generation.
package api

//go:generate go tool oapi-codegen --config oapi-codegen.yaml openapi.yaml
//go:generate go tool oapi-codegen --config oapi-codegen-client.yaml openapi.yaml
