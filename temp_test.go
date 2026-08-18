package main

import (
	"fmt"
	"testing"
)

func TestScratch(t *testing.T) {
	fmt.Println("Running TestScratch...")
	systemPrompt, err := generateSystemPrompt(defaultSystem, ".")
	if err != nil {
		t.Fatalf("Failed to generate system prompt: %v", err)
	}
	fmt.Println("System Prompt:")
	fmt.Println(systemPrompt)
}
