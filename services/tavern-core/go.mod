module github.com/Play-Hireling/hireling/services/tavern-core

go 1.26.0

require (
	github.com/Play-Hireling/hireling/runtime v0.0.0
	github.com/redis/go-redis/v9 v9.23.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/Play-Hireling/hireling/runtime => ../../runtime
