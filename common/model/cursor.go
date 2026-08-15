package model

import (
	"fmt"
	"os"
	"time"

	"aliang.one/nursorgate/common/logger"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

var db *gorm.DB

func InitDB() {
	dbname := "autocursor"
	if os.Getenv("dbname") != "" {
		dbname = os.Getenv("dbname")
	}

	dbuser := "root"
	if os.Getenv("dbuser") != "" {
		dbuser = os.Getenv("dbuser")
	}

	dbpasswd := "asd123456"
	if os.Getenv("dbpasswd") != "" {
		dbpasswd = os.Getenv("dbpasswd")
	}

	dbhost := "172.16.238.2"
	if os.Getenv("dbhost") != "" {
		dbhost = os.Getenv("dbhost")
	}

	dbport := "31494"
	if os.Getenv("dbport") != "" {
		dbport = os.Getenv("dbport")
	}

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		dbuser, dbpasswd, dbhost, dbport, dbname)

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to connect to database: %v", err))
		panic(err)

	}
	SetDB(db)
}

func SetDB(database *gorm.DB) {
	db = database
}

func GetDB() *gorm.DB {
	return db
}

type Cursor struct {
	gorm.Model
	ID              string    `json:"id" gorm:"primary_key" `
	CreatedAt       time.Time `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt       time.Time `json:"updated_at" gorm:"autoUpdateTime"`
	Membership      string    `json:"membership"`
	Usage           int       `json:"usage"`
	Name            string    `json:"name"`
	Password        string    `json:"password"`
	CursorID        string    `json:"cursor_id"`
	FirstName       string    `json:"first_name"`
	LastName        string    `json:"last_name"`
	Email           string    `json:"email"`
	AccessToken     string    `json:"access_token"`
	RefreshToken    string    `json:"refresh_token"`
	MembershipType  string    `json:"membership_type"`
	CacheEmail      bool      `json:"cache_email"`
	UniqueCppUserID string    `json:"unique_cpp_user_id"`
	DispatchOrder   int       `json:"dispatch_order"`
	Description     string    `json:"description"`
	ExpiresAt       time.Time `json:"expires_at"`
}

type CursorUrlQueryRecord struct {
	gorm.Model
	CursorID  string    `json:"cursor_id"`
	UserID    string    `json:"user_id"`
	URL       string    `json:"url"`
	Count     int       `json:"count"`
	Date      time.Time `json:"date"`
	CreatedAt time.Time `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt time.Time `json:"updated_at" gorm:"autoUpdateTime"`
}

type UserCursorModelUsage struct {
	gorm.Model
	CursorID  string    `json:"cursor_id"`
	UserID    string    `json:"user_id"`
	AskCount  int       `json:"ask_count"`
	ModelName string    `json:"model_name"`
	CreatedAt time.Time `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt time.Time `json:"updated_at" gorm:"autoUpdateTime"`
}

type UserCursorAccountBind struct {
	gorm.Model
	UserID     string    `json:"user_id"`
	CursorID   string    `json:"cursor_id"`
	Status     bool      `json:"status"`
	AskCount   int       `json:"ask_count"`
	TokenUsage int       `json:"token_usage"`
	VipLevel   int       `json:"vip_level"`
	CreatedAt  time.Time `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt  time.Time `json:"updated_at" gorm:"autoUpdateTime"`
}
