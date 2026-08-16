package model

import "time"

type FailureKind string

const (
	FailureGeo         FailureKind = "geo_rejected"
	FailureDNS         FailureKind = "dns"
	FailureTimeout     FailureKind = "timeout"
	FailureUnavailable FailureKind = "unavailable"
	FailureConnection  FailureKind = "connection"
)

type FailureEvent struct {
	Kind        FailureKind `json:"kind"`
	OccurredAt  time.Time   `json:"occurred_at"`
	Fingerprint string      `json:"fingerprint"`
}

type SuccessEvent struct {
	OccurredAt time.Time `json:"occurred_at"`
}

type Candidate struct {
	RuntimeName      string    `json:"-"`
	Name             string    `json:"name"`
	Type             string    `json:"type"`
	DelayMS          int       `json:"delay_ms"`
	Score            int64     `json:"score"`
	CooldownUntil    time.Time `json:"cooldown_until,omitempty"`
	KnownSuccess     bool      `json:"known_success"`
	SubscriptionUID  string    `json:"subscription_uid,omitempty"`
	SubscriptionName string    `json:"subscription_name,omitempty"`
	Preferred        bool      `json:"preferred"`
	Fallback         bool      `json:"fallback"`
}
