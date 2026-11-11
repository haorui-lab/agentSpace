package as

// Principal is the authenticated caller. Agent tokens are bound to one space.
type Principal struct {
	Kind    string
	UserID  string
	AgentID string
	SpaceID string
	Status  string
}

const (
	KindAdmin = "admin"
	KindAgent = "agent"
)

func (p Principal) ActorID() string {
	if p.Kind == KindAgent {
		return p.AgentID
	}
	return p.UserID
}

type Space struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Goal        string `json:"goal"`
	Description string `json:"description"`
	RunRef      string `json:"run_ref"`
	OwnerID     string `json:"owner_id,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type Invitation struct {
	ID        string  `json:"id"`
	SpaceID   string  `json:"space_id,omitempty"`
	Status    string  `json:"status"`
	CreatedBy string  `json:"created_by,omitempty"`
	ExpiresAt *string `json:"expires_at"`
	CreatedAt string  `json:"created_at"`
	RevokedAt *string `json:"revoked_at"`
	Code      string  `json:"invite_code,omitempty"`
	URL       string  `json:"invite_url,omitempty"`
}

type Member struct {
	AgentID           string  `json:"agent_id"`
	DisplayName       string  `json:"display_name"`
	Runtime           string  `json:"runtime"`
	InstanceID        string  `json:"instance_id"`
	OwnerLabel        string  `json:"owner_label"`
	Description       string  `json:"description"`
	JoinedAt          string  `json:"joined_at"`
	Status            string  `json:"status"`
	LastSeenAt        *string `json:"last_seen_at"`
	InvitationID      string  `json:"invitation_id,omitempty"`
	InvitedBy         string  `json:"invited_by,omitempty"`
	InvitedByUsername string  `json:"invited_by_username,omitempty"`
}

type DocIndex struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Revision  int    `json:"revision"`
	Pinned    bool   `json:"pinned"`
	CreatedBy string `json:"created_by"`
	UpdatedBy string `json:"updated_by"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Excerpt   string `json:"excerpt"`
}

type Document struct {
	ID        string  `json:"id"`
	SpaceID   string  `json:"space_id"`
	Title     string  `json:"title"`
	ContentMD string  `json:"content_md"`
	Revision  int     `json:"revision"`
	Pinned    bool    `json:"pinned"`
	CreatedBy string  `json:"created_by"`
	UpdatedBy string  `json:"updated_by"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	DeletedAt *string `json:"deleted_at,omitempty"`
}

type Revision struct {
	DocumentID string `json:"document_id"`
	Revision   int    `json:"revision"`
	Title      string `json:"title"`
	ContentMD  string `json:"content_md"`
	Pinned     bool   `json:"pinned"`
	EditorID   string `json:"editor_id"`
	ChangeNote string `json:"change_note"`
	CreatedAt  string `json:"created_at"`
}

type Asset struct {
	ID          string `json:"id"`
	SpaceID     string `json:"space_id"`
	Filename    string `json:"filename"`
	MimeType    string `json:"mime_type"`
	Size        int64  `json:"size"`
	UploadedBy  string `json:"uploaded_by"`
	CreatedAt   string `json:"created_at"`
	DownloadURL string `json:"download_url"`
}

type Room struct {
	ID        string   `json:"id"`
	SpaceID   string   `json:"space_id"`
	Type      string   `json:"type"`
	MemberIDs []string `json:"member_ids"`
	CreatedAt string   `json:"created_at"`
}

type DocRef struct {
	DocumentID string `json:"document_id"`
	Revision   int    `json:"revision"`
}

type Message struct {
	ID                string   `json:"id"`
	RoomID            string   `json:"room_id"`
	SenderID          string   `json:"sender_id"`
	SenderDisplayName string   `json:"sender_display_name"`
	ContentMD         string   `json:"content_md"`
	ReplyToMessageID  string   `json:"reply_to_message_id,omitempty"`
	Mentions          []string `json:"mentions"`
	DocumentRefs      []DocRef `json:"document_refs"`
	AssetIDs          []string `json:"asset_ids"`
	CreatedAt         string   `json:"created_at"`
	IdempotencyKey    string   `json:"idempotency_key,omitempty"`
	Replayed          bool     `json:"replayed,omitempty"`
}

type Event struct {
	Seq        int64  `json:"seq"`
	Type       string `json:"type"`
	ResourceID string `json:"resource_id"`
	ActorID    string `json:"actor_id"`
	RoomID     string `json:"room_id,omitempty"`
	Revision   int    `json:"revision,omitempty"`
	Summary    string `json:"summary,omitempty"`
	CreatedAt  string `json:"created_at"`
	Visibility string `json:"-"`
	SpaceID    string `json:"-"`
}

type PullResult struct {
	Events    []Event `json:"events"`
	NextSeq   int64   `json:"next_seq"`
	HasMore   bool    `json:"has_more"`
	ServerSeq int64   `json:"server_seq"`
}

type AuditEntry struct {
	ID         string  `json:"id"`
	SpaceID    *string `json:"space_id"`
	ActorID    string  `json:"actor_id"`
	Action     string  `json:"action"`
	ObjectType string  `json:"object_type"`
	ObjectID   string  `json:"object_id"`
	Result     string  `json:"result"`
	CreatedAt  string  `json:"created_at"`
}
