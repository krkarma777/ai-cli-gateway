package process

import "errors"

const windowsAncestorDangerousAccess = uint32(
	0x00010000 | // DELETE: source rename/delete authority.
		0x00040000 | // WRITE_DAC: can grant rename/delete authority.
		0x00080000 | // WRITE_OWNER: can acquire DACL authority.
		0x00000040 | // FILE_DELETE_CHILD: child rename/delete authority.
		0x10000000 | // GENERIC_ALL.
		0x02000000, // MAXIMUM_ALLOWED.
)

type windowsAncestorGrant struct {
	access  uint32
	trusted bool
}

func validateWindowsAncestorAuthority(
	ownerTrusted bool,
	grants []windowsAncestorGrant,
) error {
	if !ownerTrusted {
		return errors.New("windows ancestor is owned by an untrusted principal")
	}
	for _, grant := range grants {
		if !grant.trusted &&
			grant.access&windowsAncestorDangerousAccess != 0 {
			return errors.New("unsafe Windows ancestor mutation grant")
		}
	}
	return nil
}
