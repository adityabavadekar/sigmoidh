package main

import "github.com/gen2brain/beeep"

import (
	"fmt"
	"os/exec"
	"runtime"
)

const PING_NOTIFICATION_TITLE = "Sigmoidh | Ping"
const PING_NOTIFICATION_MESSAGE = "Task completed."

func SendNotification(title, message string) {
	if err := beeep.Notify(title, message, ""); err != nil {
		println("\a" + message)
	}
}

func Speak(text string) error {
	switch runtime.GOOS {
	case "linux":
		return exec.Command("espeak-ng", text).Run()

	case "darwin":
		return exec.Command("say", text).Run()

	case "windows":
		script := fmt.Sprintf(`
Add-Type -AssemblyName System.Speech
$s = New-Object System.Speech.Synthesis.SpeechSynthesizer
$s.Speak(%q)
`, text)

		return exec.Command(
			"powershell",
			"-NoProfile",
			"-Command",
			script,
		).Run()

	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

func PingUser() {
	title := PING_NOTIFICATION_TITLE
	message := PING_NOTIFICATION_MESSAGE
	SendNotification(title, message)
	Speak(message)
}
