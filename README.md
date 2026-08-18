# Sigmoidh

Simple Harness in Go. Tools: bash, read, write.

## Build

```bash
go mod tidy
go build -o sigmoidh .
```

## Run

Export the API key and run:

```bash
./sigmoidh
```

## Tests

Tests run dangerous commands such as `rm -rf`, so running commands in a container is must.

1. Build container
```
docker build  -t sigmoidh-test . -f Dockerfile.test
```

2. Use `test.sh` 

This runs test suite in a Docker container with a shared volume, so go does not fetch modules again and again. 
```
chmod +x test.sh
./test.sh
```

In case modules were updated, repeat Steps 1 and 2.

## Supported Providers
- OpenAI
- Anthropic
- OpenRouter

Although the project uses LiteLLM module which supports most of well known providers, `provider.go` needs to be updated to handle the provider-specific calling.

You can also use `task` tool.