#!/bin/bash

CGO_ENABLED=0 go build -o scoreproxy -ldflags '-extldflags "-static"' main.go
strip scoreproxy