package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// ContractTrustApproved records that the project approved the exact contract
// revision associated with unattended work.
const ContractTrustApproved = "approved"

// ContractApproval is the project owner's persisted decision for one exact
// parsed contract revision.
type ContractApproval struct {
	Project    string    `json:"project"`
	Revision   string    `json:"revision"`
	ApprovedAt time.Time `json:"approved_at"`
}

type contractApprovalRecord struct {
	Schema    int                `json:"schema"`
	Approvals []ContractApproval `json:"approvals"`
}

// ContractApproval returns the project owner's approval for a project, if any.
func (s *Store) ContractApproval(project string) (*ContractApproval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.readContractApprovalsLocked()
	if err != nil {
		return nil, err
	}
	for _, approval := range record.Approvals {
		if approval.Project == project {
			copy := approval
			return &copy, nil
		}
	}
	return nil, nil
}

// ApproveContract records the exact parsed contract revision approved by the
// project owner. A later approval replaces the previous revision.
func (s *Store) ApproveContract(project, revision string, now time.Time) (*ContractApproval, error) {
	if project == "" || revision == "" {
		return nil, errors.New("project and contract revision are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.readContractApprovalsLocked()
	if err != nil {
		return nil, err
	}
	approval := ContractApproval{Project: project, Revision: revision, ApprovedAt: now.UTC()}
	for i := range record.Approvals {
		if record.Approvals[i].Project == project {
			record.Approvals[i] = approval
			return &approval, s.writeContractApprovalsLocked(record)
		}
	}
	record.Approvals = append(record.Approvals, approval)
	return &approval, s.writeContractApprovalsLocked(record)
}

func (s *Store) readContractApprovalsLocked() (contractApprovalRecord, error) {
	path := filepath.Join(s.root, "contract-approvals.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return contractApprovalRecord{Schema: 1}, nil
	}
	if err != nil {
		return contractApprovalRecord{}, err
	}
	var record contractApprovalRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return contractApprovalRecord{}, err
	}
	if record.Schema != 1 {
		return contractApprovalRecord{}, errors.New("unsupported contract approval record schema")
	}
	return record, nil
}

func (s *Store) writeContractApprovalsLocked(record contractApprovalRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(s.root, "contract-approvals.json"), data, 0o600)
}
