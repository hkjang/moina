package model

import (
	"encoding/json"
	"time"
)

const (
	RoleSuperAdmin = "super_admin"
	RoleAdmin      = "admin"
	RoleTeamLead   = "team_lead"
	RoleModerator  = "moderator"
	RoleMember     = "member"
)

type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	DisplayName  string    `json:"displayName"`
	Email        string    `json:"email,omitempty"`
	Bio          string    `json:"bio,omitempty"`
	AvatarID     string    `json:"avatarId,omitempty"`
	AccountType  string    `json:"accountType"`
	Provider     string    `json:"provider,omitempty"`
	Roles        []string  `json:"roles,omitempty"`
	Active       bool      `json:"active"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	FollowerCnt  int64     `json:"followerCount,omitempty"`
	FollowingCnt int64     `json:"followingCount,omitempty"`
	MoinCnt      int64     `json:"moinCount,omitempty"`
	Following    bool      `json:"following,omitempty"`
}

type UserAuth struct {
	User
	PasswordHash string
}

type Session struct {
	ID        string
	UserID    string
	TokenHash string
	CSRFHash  string
	ExpiresAt time.Time
}

type APIKey struct {
	ID          string     `json:"id"`
	UserID      string     `json:"userId"`
	Name        string     `json:"name"`
	Prefix      string     `json:"prefix"`
	Permissions []string   `json:"permissions"`
	Version     int        `json:"version"`
	CreatedAt   time.Time  `json:"createdAt"`
	RotatedAt   time.Time  `json:"rotatedAt"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
	TokenHash   string     `json:"-"`
}

type SettingRecord struct {
	Key       string
	Payload   []byte
	Sensitive bool
	Revision  int64
	UpdatedBy string
	UpdatedAt time.Time
}

type OIDCConfig struct {
	Enabled             bool                `json:"enabled"`
	IssuerURL           string              `json:"issuerUrl"`
	ClientID            string              `json:"clientId"`
	ClientSecret        string              `json:"clientSecret,omitempty"`
	ClearClientSecret   bool                `json:"clearClientSecret,omitempty"`
	RedirectURL         string              `json:"redirectUrl,omitempty"`
	Scopes              []string            `json:"scopes"`
	AutoProvision       bool                `json:"autoProvision"`
	DefaultRoles        []string            `json:"defaultRoles"`
	RoleClaim           string              `json:"roleClaim,omitempty"`
	RoleMappings        map[string][]string `json:"roleMappings,omitempty"`
	AllowedHosts        []string            `json:"allowedHosts,omitempty"`
	PrivateAllowedHosts []string            `json:"privateAllowedHosts,omitempty"`
	AllowInsecureHTTP   bool                `json:"allowInsecureHttp,omitempty"`
}

type AIConfig struct {
	Enabled             bool     `json:"enabled"`
	BaseURL             string   `json:"baseUrl"`
	APIKey              string   `json:"apiKey,omitempty"`
	ClearAPIKey         bool     `json:"clearApiKey,omitempty"`
	Model               string   `json:"model"`
	APIStyle            string   `json:"apiStyle"`
	DefaultMaxTokens    int      `json:"defaultMaxTokens"`
	MaxTokens           int      `json:"maxTokens"`
	TimeoutSeconds      int      `json:"timeoutSeconds"`
	AllowedHosts        []string `json:"allowedHosts,omitempty"`
	PrivateAllowedHosts []string `json:"privateAllowedHosts,omitempty"`
	AllowInsecureHTTP   bool     `json:"allowInsecureHttp,omitempty"`
}

type WorkflowConfig struct {
	Enabled       bool     `json:"enabled"`
	Actions       []string `json:"actions"`
	ApproverRoles []string `json:"approverRoles"`
}

type Moin struct {
	ID               string                 `json:"id"`
	Author           User                   `json:"author"`
	AuthorID         string                 `json:"authorId"`
	Content          string                 `json:"content"`
	Kind             string                 `json:"kind"`
	Visibility       string                 `json:"visibility"`
	Status           string                 `json:"status"`
	ReplyToID        string                 `json:"replyToId,omitempty"`
	QuoteMoinID      string                 `json:"quoteMoinId,omitempty"`
	RemoinMoinID     string                 `json:"remoinMoinId,omitempty"`
	MoimID           string                 `json:"moimId,omitempty"`
	ApprovalRequired bool                   `json:"approvalRequired"`
	Media            []Media                `json:"media"`
	Topics           []Topic                `json:"topics"`
	Signals          map[string]int64       `json:"signals"`
	ReplyCount       int64                  `json:"replyCount"`
	RemoinCount      int64                  `json:"remoinCount"`
	Bookmarked       bool                   `json:"bookmarked"`
	ViewerSignals    []string               `json:"viewerSignals"`
	Why              []RecommendationReason `json:"why,omitempty"`
	Recommendation   []RecommendationReason `json:"recommendation,omitempty"`
	ViewerRemoined   bool                   `json:"remoined"`
	QuoteMoin        *Moin                  `json:"quoteMoin,omitempty"`
	RemoinMoin       *Moin                  `json:"remoinMoin,omitempty"`
	CreatedAt        time.Time              `json:"createdAt"`
	UpdatedAt        time.Time              `json:"updatedAt"`
}

type RecommendationReason struct {
	Label string  `json:"label"`
	Score float64 `json:"score"`
}

type Media struct {
	ID        string    `json:"id"`
	OwnerID   string    `json:"ownerId,omitempty"`
	Filename  string    `json:"filename"`
	AltText   string    `json:"altText,omitempty"`
	MIMEType  string    `json:"mimeType"`
	Type      string    `json:"type"`
	URL       string    `json:"url"`
	Size      int64     `json:"size"`
	Width     int       `json:"width,omitempty"`
	Height    int       `json:"height,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type Topic struct {
	ID            string    `json:"id"`
	Slug          string    `json:"slug"`
	Name          string    `json:"name"`
	Description   string    `json:"description,omitempty"`
	FollowerCount int64     `json:"followerCount"`
	MoinCount     int64     `json:"moinCount"`
	Following     bool      `json:"following,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
}

type Notification struct {
	ID         string          `json:"id"`
	UserID     string          `json:"userId,omitempty"`
	ActorID    string          `json:"actorId,omitempty"`
	Type       string          `json:"type"`
	TargetID   string          `json:"targetId,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	Title      string          `json:"title,omitempty"`
	Body       string          `json:"body,omitempty"`
	Actor      any             `json:"actor,omitempty"`
	TargetPath string          `json:"targetPath,omitempty"`
	ReadAt     *time.Time      `json:"readAt,omitempty"`
	CreatedAt  time.Time       `json:"createdAt"`
}

type Moim struct {
	ID          string    `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	OwnerID     string    `json:"ownerId"`
	Visibility  string    `json:"visibility"`
	MemberCount int64     `json:"memberCount"`
	MoinCount   int64     `json:"moinCount"`
	Joined      bool      `json:"joined,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

type Approval struct {
	ID          string          `json:"id"`
	Action      string          `json:"action"`
	TargetType  string          `json:"targetType"`
	TargetID    string          `json:"targetId"`
	RequesterID string          `json:"requesterId"`
	Status      string          `json:"status"`
	Snapshot    json.RawMessage `json:"snapshot,omitempty"`
	ReviewerID  string          `json:"reviewerId,omitempty"`
	Comment     string          `json:"comment,omitempty"`
	RequestedAt time.Time       `json:"requestedAt"`
	ReviewedAt  *time.Time      `json:"reviewedAt,omitempty"`
}

type Report struct {
	ID          string     `json:"id"`
	ReporterID  string     `json:"reporterId"`
	TargetType  string     `json:"targetType"`
	TargetID    string     `json:"targetId"`
	Reason      string     `json:"reason"`
	Detail      string     `json:"detail,omitempty"`
	Status      string     `json:"status"`
	Resolution  string     `json:"resolution,omitempty"`
	ModeratorID string     `json:"moderatorId,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	ResolvedAt  *time.Time `json:"resolvedAt,omitempty"`
}
