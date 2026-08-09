package idgen

import "github.com/google/uuid"

func NewFileID() string {
	return uuid.New().String()
}
