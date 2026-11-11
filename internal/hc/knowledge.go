package hc

import (
	"context"
	"sort"
	"strconv"
	"strings"
)

const maxDocumentBytes = 1_000_000

type CreateDocumentInput struct {
	Title      string
	ContentMD  string
	Pinned     bool
	ChangeNote string
}

func (s *Server) CreateDocument(ctx context.Context, p Principal, spaceID string, in CreateDocumentInput) (Document, error) {
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return Document{}, err
	}
	title, content, note, err := validateDocFields(in.Title, in.ContentMD, in.ChangeNote, true)
	if err != nil {
		return Document{}, err
	}
	ts := now()
	doc := docRecord{
		UpdatedAt: ts, Revision: 1, ID: newID("doc_"), SpaceID: spaceID, Pinned: in.Pinned,
		CreatedBy: p.ActorID(), UpdatedBy: p.ActorID(), CreatedAt: ts, Title: title, ContentMD: content,
	}
	rev := revRecord{
		DocumentID: doc.ID, Revision: 1, Title: title, ContentMD: content, Pinned: in.Pinned,
		EditorID: p.ActorID(), ChangeNote: note, CreatedAt: ts,
	}
	ev, err := s.st.createDoc(ctx, doc, rev)
	if err != nil {
		return Document{}, err
	}
	s.hub.publish(ev)
	return doc.api(), nil
}

func validateDocFields(title, content, note string, titleRequired bool) (string, string, string, error) {
	title = strings.TrimSpace(title)
	if titleRequired && title == "" {
		return "", "", "", badRequest("title is required")
	}
	if strings.Contains(title, "\x00") || len([]rune(title)) > 300 {
		return "", "", "", badRequest("title is invalid")
	}
	if strings.Contains(content, "\x00") || len(content) > maxDocumentBytes {
		return "", "", "", badRequest("content_md is too large or contains invalid characters")
	}
	note = strings.TrimSpace(note)
	if strings.Contains(note, "\x00") || len([]rune(note)) > 500 {
		return "", "", "", badRequest("change_note is invalid")
	}
	return title, content, note, nil
}

type PatchDocumentInput struct {
	Title            *string
	ContentMD        *string
	Pinned           *bool
	ExpectedRevision int
	ChangeNote       string
}

func (s *Server) UpdateDocument(ctx context.Context, p Principal, spaceID, docID string, in PatchDocumentInput) (Document, error) {
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return Document{}, err
	}
	if in.ExpectedRevision < 1 {
		return Document{}, badRequest("expected_revision or If-Match is required")
	}
	if in.Title == nil && in.ContentMD == nil && in.Pinned == nil {
		return Document{}, badRequest("no document fields to update")
	}
	cur, err := s.st.getDoc(ctx, spaceID, docID, false)
	if err != nil {
		return Document{}, err
	}
	if cur.Revision != in.ExpectedRevision {
		api := cur.api()
		return Document{}, conflict("document revision conflict", map[string]any{
			"current_revision": api.Revision,
			"document":         api,
		})
	}
	title, content, pinned := cur.Title, cur.ContentMD, cur.Pinned
	if in.Title != nil {
		title = *in.Title
	}
	if in.ContentMD != nil {
		content = *in.ContentMD
	}
	if in.Pinned != nil {
		pinned = *in.Pinned
	}
	title, content, note, err := validateDocFields(title, content, in.ChangeNote, true)
	if err != nil {
		return Document{}, err
	}
	ts := now()
	next := cur
	next.Title = title
	next.ContentMD = content
	next.Pinned = pinned
	next.Revision = cur.Revision + 1
	next.UpdatedBy = p.ActorID()
	next.UpdatedAt = ts
	rev := revRecord{
		DocumentID: docID, Revision: next.Revision, Title: title, ContentMD: content, Pinned: pinned,
		EditorID: p.ActorID(), ChangeNote: note, CreatedAt: ts,
	}
	ev, err := s.st.updateDoc(ctx, cur.Revision, next, rev)
	if err != nil {
		return Document{}, err
	}
	s.hub.publish(ev)
	return next.api(), nil
}

func (s *Server) GetDocument(ctx context.Context, p Principal, spaceID, docID string) (Document, error) {
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return Document{}, err
	}
	doc, err := s.st.getDoc(ctx, spaceID, docID, false)
	if err != nil {
		return Document{}, err
	}
	return doc.api(), nil
}

func (s *Server) DeleteDocument(ctx context.Context, p Principal, spaceID, docID string) error {
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return err
	}
	ev, err := s.st.deleteDoc(ctx, spaceID, docID, p.ActorID())
	if err != nil {
		return err
	}
	s.hub.publish(ev)
	return nil
}

func (s *Server) ListDocuments(ctx context.Context, p Principal, spaceID, q, cursor string, limit int) ([]DocIndex, string, error) {
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return nil, "", err
	}
	return s.queryDocuments(ctx, spaceID, q, cursor, limit, "all")
}

func (s *Server) listDocuments(ctx context.Context, spaceID, q, cursor string, limit int, pinnedOnly bool) ([]DocIndex, error) {
	mode := "recent"
	if pinnedOnly {
		mode = "pinned"
	}
	docs, _, err := s.queryDocuments(ctx, spaceID, q, cursor, limit, mode)
	return docs, err
}

func (s *Server) queryDocuments(ctx context.Context, spaceID, q, cursor string, limit int, mode string) ([]DocIndex, string, error) {
	q = strings.TrimSpace(q)
	if len([]rune(q)) > 200 {
		return nil, "", badRequest("query is too long")
	}
	limit = clampLimit(limit, 30, 100)
	docs, err := s.st.listDocs(ctx, spaceID)
	if err != nil {
		return nil, "", err
	}
	filtered := make([]docRecord, 0, len(docs))
	for _, doc := range docs {
		if mode == "pinned" && !doc.Pinned {
			continue
		}
		if q != "" && !containsFold(doc.Title, q) && !containsFold(doc.ContentMD, q) {
			continue
		}
		filtered = append(filtered, doc)
	}
	sort.Slice(filtered, func(i, j int) bool {
		if mode == "all" && filtered[i].Pinned != filtered[j].Pinned {
			return filtered[i].Pinned
		}
		if filtered[i].UpdatedAt != filtered[j].UpdatedAt {
			return filtered[i].UpdatedAt > filtered[j].UpdatedAt
		}
		return filtered[i].ID > filtered[j].ID
	})
	if cursor != "" {
		parts, err := decodeCursor(cursor)
		if err != nil {
			return nil, "", badRequest("invalid cursor")
		}
		if mode == "all" {
			if len(parts) != 3 {
				return nil, "", badRequest("invalid cursor")
			}
			pinned, err := strconv.Atoi(parts[0])
			if err != nil || (pinned != 0 && pinned != 1) {
				return nil, "", badRequest("invalid cursor")
			}
			filtered = filterDocs(filtered, func(d docRecord) bool {
				return docAfterCursor(d, pinned, parts[1], parts[2], true)
			})
		} else {
			if len(parts) != 2 {
				return nil, "", badRequest("invalid cursor")
			}
			filtered = filterDocs(filtered, func(d docRecord) bool {
				return docAfterCursor(d, 0, parts[0], parts[1], false)
			})
		}
	}
	next := ""
	if len(filtered) > limit {
		filtered = filtered[:limit]
		last := filtered[len(filtered)-1]
		if mode == "all" {
			next = encodeCursor(strconv.Itoa(boolInt(last.Pinned)), last.UpdatedAt, last.ID)
		} else {
			next = encodeCursor(last.UpdatedAt, last.ID)
		}
	}
	out := make([]DocIndex, 0, len(filtered))
	for _, doc := range filtered {
		out = append(out, DocIndex{
			ID: doc.ID, Title: doc.Title, Revision: doc.Revision, Pinned: doc.Pinned,
			CreatedBy: doc.CreatedBy, UpdatedBy: doc.UpdatedBy, CreatedAt: doc.CreatedAt, UpdatedAt: doc.UpdatedAt,
			Excerpt: excerpt(doc.ContentMD, 180),
		})
	}
	return out, next, nil
}

func filterDocs(docs []docRecord, keep func(docRecord) bool) []docRecord {
	out := make([]docRecord, 0, len(docs))
	for _, doc := range docs {
		if keep(doc) {
			out = append(out, doc)
		}
	}
	return out
}

func docAfterCursor(d docRecord, pinned int, updated, id string, usePinned bool) bool {
	if usePinned {
		dp := boolInt(d.Pinned)
		if dp != pinned {
			return dp < pinned
		}
	}
	if d.UpdatedAt != updated {
		return d.UpdatedAt < updated
	}
	return d.ID < id
}

func (s *Server) SearchDocuments(ctx context.Context, p Principal, spaceID, q string, limit int) ([]DocIndex, error) {
	if strings.TrimSpace(q) == "" {
		return nil, badRequest("q is required")
	}
	docs, _, err := s.ListDocuments(ctx, p, spaceID, q, "", limit)
	return docs, err
}

func (s *Server) ListRevisions(ctx context.Context, p Principal, spaceID, docID string) ([]Revision, error) {
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return nil, err
	}
	if err := s.ensureDocInSpace(ctx, spaceID, docID); err != nil {
		return nil, err
	}
	rows, err := s.st.listRevs(ctx, docID)
	if err != nil {
		return nil, err
	}
	out := make([]Revision, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.api())
	}
	return out, nil
}

func (s *Server) GetRevision(ctx context.Context, p Principal, spaceID, docID string, revision int) (Revision, error) {
	if err := s.authorizeSpace(ctx, p, spaceID); err != nil {
		return Revision{}, err
	}
	if err := s.ensureDocInSpace(ctx, spaceID, docID); err != nil {
		return Revision{}, err
	}
	rev, ok, err := s.st.getRev(ctx, docID, revision)
	if err != nil {
		return Revision{}, err
	}
	if !ok {
		return Revision{}, notFound("revision not found")
	}
	return rev.api(), nil
}

func (s *Server) ensureDocInSpace(ctx context.Context, spaceID, docID string) error {
	_, err := s.st.getDoc(ctx, spaceID, docID, true)
	return err
}
