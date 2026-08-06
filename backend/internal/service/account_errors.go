package service

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const maxAccountErrorRunes = 500

// accountErrorMessage formats a provider failure for durable admin display.
// Provider errors used here contain status/code summaries, never credentials.
// Flattening and bounding the value keeps one upstream HTML/JSON response from
// turning the account table into a log dump.
func accountErrorMessage(source string, err error) string {
	source = strings.TrimSpace(source)
	detail := ""
	if err != nil {
		detail = strings.TrimSpace(err.Error())
	}
	detail = strings.Join(strings.Fields(detail), " ")
	if source != "" && detail != "" {
		detail = source + "：" + detail
	} else if source != "" {
		detail = source
	}
	if detail == "" {
		detail = "账号已自动禁用（未记录具体上游错误）"
	}
	if utf8.RuneCountInString(detail) > maxAccountErrorRunes {
		runes := []rune(detail)
		detail = string(runes[:maxAccountErrorRunes-1]) + "…"
	}
	return detail
}

func abnormalPatch(source string, err error) map[string]any {
	return map[string]any{
		"status":     "disabled",
		"dead":       true,
		"last_error": accountErrorMessage(source, err),
	}
}

func providerAuthError(provider string) error {
	return errors.New(provider + " 认证凭证无效或已过期")
}
