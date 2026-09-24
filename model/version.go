package model

import "time"

type Version struct {
	Id        uint      `gorm:"column:id;primary_key" json:"id"`
	ChangeLog string    `json:"change_log"`
	Version   string    `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// PublishedAt is when the release was built (RFC 3339), which the automatic
	// update waits 48 hours after. A string: a value that is not a time leaves
	// the release to the button and never makes the whole version.json unreadable.
	PublishedAt string `json:"published_at,omitempty"`
}
