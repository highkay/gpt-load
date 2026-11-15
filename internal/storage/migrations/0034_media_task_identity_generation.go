package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

const ID0034 = "0034_media_task_identity_generation"

type mediaTask0034 struct {
	ID                           string `gorm:"type:varchar(40);primaryKey;not null"`
	CredentialIdentityGeneration uint64 `gorm:"column:credential_identity_generation;not null;default:0"`
}

func (mediaTask0034) TableName() string { return "media_tasks" }

func Up0034(db *gorm.DB) error {
	if !db.Migrator().HasTable(&mediaTask0034{}) {
		return fmt.Errorf("media tasks table is missing")
	}
	if !db.Migrator().HasColumn(&mediaTask0034{}, "CredentialIdentityGeneration") {
		if err := db.Migrator().AddColumn(&mediaTask0034{}, "CredentialIdentityGeneration"); err != nil {
			return fmt.Errorf("add media task credential identity generation: %w", err)
		}
	}
	return Validate0034(db)
}

func ValidateRecoverable0034(db *gorm.DB) error {
	if !db.Migrator().HasTable(&mediaTask0034{}) {
		return fmt.Errorf("media task identity generation: media tasks table is missing")
	}
	return nil
}

func Validate0034(db *gorm.DB) error {
	model := &mediaTask0034{}
	if !db.Migrator().HasTable(model) {
		return fmt.Errorf("media tasks table is missing")
	}
	if !db.Migrator().HasColumn(model, "CredentialIdentityGeneration") {
		return fmt.Errorf("media tasks credential identity generation column is missing")
	}
	return nil
}
