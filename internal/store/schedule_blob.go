package store

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Recurring send schedules keep their editable content outside the draft
// message namespace, under <base>/<account>/schedules/<schedule>/<nonce>.
// Each content revision gets its own nonce directory, so replacing a plan's
// content never mutates the files frozen instances already reference through
// their outgoing_sends snapshots.
func validScheduleID(scheduleID string) bool {
	if len(scheduleID) != 36 || strings.Count(scheduleID, "-") != 4 {
		return false
	}
	if strings.ContainsAny(scheduleID, `/\.`) || scheduleID != strings.TrimSpace(scheduleID) {
		return false
	}
	return true
}

func (s *BlobStore) schedulesDir(accountID string) string {
	return filepath.Join(s.basePath, accountID, "schedules")
}

func (s *BlobStore) scheduleDir(accountID, scheduleID string) (string, error) {
	if !validScheduleID(scheduleID) {
		return "", fmt.Errorf("invalid schedule id")
	}
	return filepath.Join(s.schedulesDir(accountID), scheduleID), nil
}

// NewScheduleVersion stages a fresh content revision. Discard it with
// DeleteScheduleVersion if the database commit never happens.
func (s *BlobStore) NewScheduleVersion(accountID, scheduleID string) (*BlobStore, error) {
	dir, err := s.scheduleDir(accountID, scheduleID)
	if err != nil {
		return nil, err
	}
	version, err := s.NewMessageVersion()
	if err != nil {
		return nil, err
	}
	versionDir := filepath.Join(dir, version.messageVersion)
	if err := version.ensureDir(versionDir); err != nil {
		return nil, fmt.Errorf("create schedule dir: %w", err)
	}
	return version, nil
}

// ScheduleVersionDir reports the revision directory of a versioned store.
func (s *BlobStore) ScheduleVersionDir(accountID, scheduleID string) (string, error) {
	dir, err := s.scheduleDir(accountID, scheduleID)
	if err != nil {
		return "", err
	}
	if s.messageVersion == "" {
		return "", fmt.Errorf("not a schedule version")
	}
	return filepath.Join(dir, s.messageVersion), nil
}

// StoreScheduleAttachment writes one plan attachment into the revision store.
func (s *BlobStore) StoreScheduleAttachment(ctx context.Context, accountID, scheduleID string, attID int64, filename string, r io.Reader) (string, error) {
	versionDir, err := s.ScheduleVersionDir(accountID, scheduleID)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(versionDir, "attachments")
	if err := s.ensureDir(dir); err != nil {
		return "", fmt.Errorf("create schedule attachments dir: %w", err)
	}
	sanitized := sanitizeFilename(filename)
	if sanitized == "" {
		sanitized = fmt.Sprintf("attachment-%d", attID)
	}
	p := filepath.Join(dir, fmt.Sprintf("%d-%s", attID, sanitized))
	f, err := os.Create(p)
	if err != nil {
		return "", fmt.Errorf("create attachment file: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		_ = os.Remove(p)
		return "", fmt.Errorf("write attachment: %w", err)
	}
	return p, nil
}

// DeleteScheduleVersion removes one staged or superseded content revision.
func (s *BlobStore) DeleteScheduleVersion(accountID, scheduleID, nonce string) error {
	dir, err := s.scheduleDir(accountID, scheduleID)
	if err != nil {
		return err
	}
	if nonce == "" || strings.ContainsAny(nonce, `/\`) || !versionName(nonce) {
		return fmt.Errorf("invalid schedule version")
	}
	if err := os.RemoveAll(filepath.Join(dir, nonce)); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) == 0 {
		_ = os.Remove(dir)
	}
	return nil
}

// VersionNonce reports the revision directory of a versioned blob store.
func (s *BlobStore) VersionNonce() string {
	return s.messageVersion
}

// DeleteSchedule removes every content revision of a plan. It never errors
// when the directory was already gone.
func (s *BlobStore) DeleteSchedule(accountID, scheduleID string) error {
	dir, err := s.scheduleDir(accountID, scheduleID)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// DeleteScheduleVersionsExcept removes staged or superseded revisions,
// keeping only the named one. It is best effort and ignores missing dirs.
func (s *BlobStore) DeleteScheduleVersionsExcept(accountID, scheduleID, keepNonce string) error {
	dir, err := s.scheduleDir(accountID, scheduleID)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !versionName(entry.Name()) || entry.Name() == keepNonce {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
