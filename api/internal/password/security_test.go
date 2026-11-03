package password

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPasswordQueueRejectsExcessWork(t *testing.T) {
	for i := 0; i < cap(hashQueue); i++ {
		hashQueue <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(hashQueue); i++ {
			<-hashQueue
		}
	}()
	start := time.Now()
	_, err := New().Hash("secret-password")
	require.ErrorIs(t, err, ErrBusy)
	require.Less(t, time.Since(start), time.Second)
}
func TestRejectUnboundedHashParameters(t *testing.T) {
	for _, value := range []string{
		"$argon2id$v=19$m=4294967295,t=1,p=1$MTIzNDU2Nzg$MTIzNDU2Nzg5MDEyMzQ1Ng",
		"$argon2id$v=19$m=65536,t=100000,p=1$MTIzNDU2Nzg$MTIzNDU2Nzg5MDEyMzQ1Ng",
		"$argon2id$v=19$m=-1,t=1,p=1$MTIzNDU2Nzg$MTIzNDU2Nzg5MDEyMzQ1Ng",
	} {
		_, err := New().Verify("password", value)
		require.Error(t, err)
	}
}
