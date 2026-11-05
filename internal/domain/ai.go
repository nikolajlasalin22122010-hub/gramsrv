package domain

import "errors"

const (
	// AICompose 最大文本长度沿用普通消息正文限制，避免 provider 请求被客户端巨大值打爆。
	MaxAIComposeTextLength = MaxMessageTextLength
	// MaxAIComposeEntityCount 限制 TextWithEntities 的实体数量。
	MaxAIComposeEntityCount = MaxMessageEntityCount
	// AICompose tone 的 appConfig 默认值与 TDesktop/DrKLO 消费点一致。
	MaxAIComposeToneTitleLength    = 12
	MaxAIComposeTonePromptLength   = 1024
	AIComposeToneSavedLimitDefault = 5
	AIComposeToneSavedLimitPremium = 20
	AIComposeToneExamplesNum       = 3
)

var (
	ErrAIComposeDisabled            = errors.New("ai compose disabled")
	ErrAIComposeInvalid             = errors.New("ai compose invalid")
	ErrAIComposeToneInvalid         = errors.New("ai compose tone invalid")
	ErrAIComposeToneNotFound        = errors.New("ai compose tone not found")
	ErrAIComposeToneLimitExceeded   = errors.New("ai compose tone limit exceeded")
	ErrAIComposeRateLimited         = errors.New("ai compose rate limited")
	ErrAIComposeProviderUnavailable = errors.New("ai compose provider unavailable")
	ErrAIComposeProviderTimeout     = errors.New("ai compose provider timeout")
)

type AIComposeText struct {
	Text     string
	Entities []MessageEntity
}

func (t AIComposeText) Clone() AIComposeText {
	out := t
	out.Entities = append([]MessageEntity(nil), t.Entities...)
	return out
}

// AIAttachmentKind 标识随请求一起送进模型的多模态内容类型。目前只有图片是
// OpenAI 兼容 chat/completions 里标准化的部分：音频（input_audio）和 PDF
// （file）是各家自行的扩展，公开端点普遍不支持，所以不在这里定义。
type AIAttachmentKind string

const (
	AIAttachmentImage AIAttachmentKind = "image"
)

// AIAttachment 是一份随请求发送的媒体内容。Data 是原始字节，Provider 负责
// 按目标 API 的形态编码（OpenAI 兼容端点用 data: URL 的 base64）。
type AIAttachment struct {
	Kind AIAttachmentKind
	MIME string
	Data []byte
}

type AIComposeToneRefKind string

const (
	AIComposeToneRefDefault AIComposeToneRefKind = "default"
	AIComposeToneRefID      AIComposeToneRefKind = "id"
	AIComposeToneRefSlug    AIComposeToneRefKind = "slug"
)

type AIComposeToneRef struct {
	Kind        AIComposeToneRefKind
	DefaultTone string
	ID          int64
	AccessHash  int64
	Slug        string
}

func (r AIComposeToneRef) Empty() bool {
	return r.Kind == "" && r.DefaultTone == "" && r.ID == 0 && r.Slug == ""
}

type AIComposeRequest struct {
	UserID          int64
	Text            AIComposeText
	Proofread       bool
	Emojify         bool
	TranslateToLang string
	Tone            AIComposeToneRef
	// Attachments carries media through to the provider. Only the text
	// generation path fills it today.
	Attachments []AIAttachment
}

type AIComposeResult struct {
	ResultText AIComposeText
	DiffText   *AIComposeText
}

type AITextGenerationRequest struct {
	UserID      int64
	Text        AIComposeText
	Instruction string
	// Attachments 是可选的随附媒体。为空时请求与纯文本完全一致，所以旧调用方
	// 不需要任何改动。
	Attachments []AIAttachment
}

type AIComposeTone struct {
	Default        bool
	Creator        bool
	ID             int64
	AccessHash     int64
	OwnerUserID    int64
	Slug           string
	Title          string
	EmojiID        int64
	Prompt         string
	InstallsCount  int
	AuthorID       int64
	DisplayAuthor  bool
	CreatedAt      int64
	UpdatedAt      int64
	Saved          bool
	ExampleEnglish *AIComposeToneExample
}

func (t AIComposeTone) Clone() AIComposeTone {
	out := t
	if t.ExampleEnglish != nil {
		ex := t.ExampleEnglish.Clone()
		out.ExampleEnglish = &ex
	}
	return out
}

type AIComposeToneExample struct {
	From AIComposeText
	To   AIComposeText
}

func (e AIComposeToneExample) Clone() AIComposeToneExample {
	return AIComposeToneExample{
		From: e.From.Clone(),
		To:   e.To.Clone(),
	}
}

type AIComposeToneInput struct {
	UserID        int64
	DisplayAuthor bool
	EmojiID       int64
	Title         string
	Prompt        string
}

type AIComposeToneUpdate struct {
	Ref           AIComposeToneRef
	UserID        int64
	DisplayAuthor *bool
	EmojiID       *int64
	Title         *string
	Prompt        *string
}

type AIComposeTones struct {
	Hash  int64
	Tones []AIComposeTone
}

func (t AIComposeTones) Clone() AIComposeTones {
	out := t
	out.Tones = make([]AIComposeTone, 0, len(t.Tones))
	for _, tone := range t.Tones {
		out.Tones = append(out.Tones, tone.Clone())
	}
	return out
}
