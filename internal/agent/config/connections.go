package config

import (
	"errors"
	"os"
	"strings"
)

// ValidateConnectionFields checks the Slack pair and the GitHub App, and nothing else.
// Settings → Connections saves through this so a value the conductor would refuse at
// start is refused at the button instead. Profile directories, the server URL and the
// rest of Validate stay out of it: those are the process, not the connection.
func (c Config) ValidateConnectionFields() error {
	switch {
	case c.SlackAppToken != "" && c.SlackBotToken == "":
		return errors.New("PODIUM_AGENT_SLACK_BOT_TOKEN is missing: Socket Mode needs both the " +
			"app-level token (xapp-…) and the bot token (xoxb-…)")
	case c.SlackBotToken != "" && c.SlackAppToken == "":
		return errors.New("PODIUM_AGENT_SLACK_APP_TOKEN is missing: Socket Mode needs both the " +
			"app-level token (xapp-…) and the bot token (xoxb-…)")
	}
	if err := c.validateGitHubApp(); err != nil {
		return err
	}
	return c.validateGitHubSource()
}

// deprecatedGitHubEnv are the variables that used to configure the GitHub App. Settings →
// Connections is the only place that does now. They stay named so a deployment that still
// sets them gets a startup warning instead of a silent App.
var deprecatedGitHubEnv = []string{
	"PODIUM_AGENT_GITHUB_APP_ID",
	"PODIUM_AGENT_GITHUB_APP_KEY_FILE",
	"PODIUM_AGENT_GITHUB_APP_KEY",
	"PODIUM_AGENT_GITHUB_WEBHOOK_SECRET",
	"PODIUM_AGENT_GITHUB_WEBHOOK_LISTEN",
}

// DeprecatedGitHubEnv returns the deprecated GitHub variables that are set and non-empty.
// The values are not returned: a log line names the variable, never the secret.
func DeprecatedGitHubEnv() []string {
	var set []string
	for _, name := range deprecatedGitHubEnv {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			set = append(set, name)
		}
	}
	return set
}
