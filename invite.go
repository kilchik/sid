package main

import "strings"

const invitationPrefix = "MBI"
const inviteCodeLen = 36

func parseInviteCode(text string) string {
	start := strings.Index(text, invitationPrefix)
	if start == -1 {
		return ""
	}
	start += 4
	end := start + inviteCodeLen
	return text[start:end]
}
