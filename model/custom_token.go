package model

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

var ErrCustomOrderExists = errors.New("Order number already has an API key")

// BeforeCreate normalizes metadata for every token creation path.
func (token *Token) BeforeCreate(_ *gorm.DB) error {
	if token.CustomOrderNo != nil {
		order := strings.TrimSpace(string(*token.CustomOrderNo))
		if !utf8.ValidString(order) || len(order) > 128 || strings.ContainsAny(order, "\x00\r\n") {
			return errors.New("Invalid order number (maximum 128 bytes)")
		}
		token.CustomOrderNo = nil
		if order != "" {
			value := CustomCaseSensitiveString(order)
			token.CustomOrderNo = &value
		}
	}
	token.CustomPhone = strings.TrimSpace(token.CustomPhone)
	if len(token.CustomPhone) > 32 {
		return errors.New("Invalid phone number")
	}
	if token.CustomPhone != "" {
		digits := 0
		for i, char := range token.CustomPhone {
			switch {
			case char >= '0' && char <= '9':
				digits++
			case char == '+' && i == 0:
			case char == ' ' || char == '-' || char == '(' || char == ')':
			default:
				return errors.New("Invalid phone number")
			}
		}
		if digits < 7 || digits > 15 {
			return errors.New("Invalid phone number")
		}
	}
	return nil
}

// CustomCaseSensitiveString preserves exact order/share identity across databases.
type CustomCaseSensitiveString string

func (CustomCaseSensitiveString) GormDataType() string { return "string" }

func (CustomCaseSensitiveString) GormDBDataType(db *gorm.DB, field *schema.Field) string {
	typ := fmt.Sprintf("varchar(%d)", field.Size)
	if db.Dialector.Name() == "mysql" {
		return typ + " CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"
	}
	return typ
}

func (token *Token) ActiveCustomShareCode() string {
	if token.CustomShareCode == nil || strings.HasPrefix(string(*token.CustomShareCode), "-") {
		return ""
	}
	return string(*token.CustomShareCode)
}

func (token *Token) RegenerateCustomShare() error {
	for range 5 {
		code, err := common.GenerateRandomCharsKey(8)
		if err != nil {
			return err
		}
		var count int64
		if err := DB.Session(&gorm.Session{Logger: DB.Logger.LogMode(logger.Silent)}).Unscoped().Model(&Token{}).Where("custom_share_code IN ?", []string{code, "-" + code}).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			value := CustomCaseSensitiveString(code)
			token.CustomShareCode = &value
			return nil
		}
	}
	return errors.New("Unable to generate a unique share code")
}

func GetTokenByCustomShare(code string) (*Token, error) {
	if len(code) != 8 {
		return nil, gorm.ErrRecordNotFound
	}
	for _, char := range code {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9') {
			return nil, gorm.ErrRecordNotFound
		}
	}
	var token Token
	err := DB.Session(&gorm.Session{Logger: DB.Logger.LogMode(logger.Silent)}).Where("custom_share_code = ?", code).First(&token).Error
	return &token, err
}

func (token *Token) UpdateCustomShare() error {
	return DB.Session(&gorm.Session{Logger: DB.Logger.LogMode(logger.Silent)}).
		Model(token).Where("user_id = ?", token.UserId).
		Select("custom_share_code").Updates(token).Error
}
