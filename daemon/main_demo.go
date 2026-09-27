//go:build demo

package main

import "omarchy-omamessages/providers/fake"

func init() { demoFactories = fake.DemoFactories }
