#!/bin/bash

CGO_ENABLED=0 go build -o scoreproxy -ldflags '-s -w' main.go