module github.com/arnav127/iima-platform/dev

go 1.27.0

require (
	github.com/arnav127/iima-platform/core v0.0.0
	github.com/arnav127/iima-platform/kit v0.0.0
)

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/go-jose/go-jose/v4 v4.1.6 // indirect
)

replace github.com/arnav127/iima-platform/kit => ../kit

replace github.com/arnav127/iima-platform/core => ../core
