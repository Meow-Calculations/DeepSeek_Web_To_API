package config

import "strings"

func (a Account) Identifier() string {
	a = NormalizeAccountIdentity(a)
	if strings.TrimSpace(a.Email) != "" {
		return strings.TrimSpace(a.Email)
	}
	if strings.TrimSpace(a.Mobile) != "" {
		return strings.TrimSpace(a.Mobile)
	}
	return ""
}

func NormalizeAccountIdentity(a Account) Account {
	a.Email = strings.TrimSpace(a.Email)
	a.Mobile = strings.TrimSpace(a.Mobile)
	if a.Email == "" && looksLikeEmailIdentifier(a.Mobile) {
		a.Email = a.Mobile
		a.Mobile = ""
		return a
	}
	a.Mobile = NormalizeMobileForStorage(a.Mobile)
	return a
}

func looksLikeEmailIdentifier(raw string) bool {
	return strings.Contains(strings.TrimSpace(raw), "@")
}
