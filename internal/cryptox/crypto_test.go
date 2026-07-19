package cryptox

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
)

func TestEncryptObjectRoundTripAcrossFrames(t *testing.T) {
	salt, err := RandomSalt()
	if err != nil {
		t.Fatal(err)
	}
	key, err := DeriveKey("test password", salt, DefaultKDFParams())
	if err != nil {
		t.Fatal(err)
	}
	header, err := NewHeader("task", "object", "data", EncodeSalt(salt))
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("abcdef0123456789"), FrameSize/8)
	metadata := map[string]any{"name": "图片/测试.jpg", "size": len(payload)}
	var encrypted bytes.Buffer
	result, err := EncryptObject(&encrypted, key, header, metadata, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if result.Size != int64(encrypted.Len()) || len(result.SHA256) != 64 {
		t.Fatalf("unexpected result: %+v", result)
	}
	reader, err := OpenObject(bytes.NewReader(encrypted.Bytes()), "test password")
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(reader.Metadata, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["name"] != "图片/测试.jpg" {
		t.Fatalf("unexpected metadata: %#v", decoded)
	}
	restored, err := io.ReadAll(reader.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, restored) {
		t.Fatal("payload mismatch")
	}
}

func TestWrongPasswordAndTamperingFailAuthentication(t *testing.T) {
	salt, _ := RandomSalt()
	key, _ := DeriveKey("correct", salt, DefaultKDFParams())
	header, _ := NewHeader("task", "object", "data", EncodeSalt(salt))
	var encrypted bytes.Buffer
	_, err := EncryptObject(&encrypted, key, header, map[string]string{"type": "test"}, bytes.NewReader([]byte("secret payload")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenObject(bytes.NewReader(encrypted.Bytes()), "wrong"); err == nil {
		t.Fatal("wrong password unexpectedly succeeded")
	}
	tampered := append([]byte(nil), encrypted.Bytes()...)
	tampered[len(tampered)-8] ^= 0xff
	reader, err := OpenObject(bytes.NewReader(tampered), "correct")
	if err == nil {
		_, err = io.ReadAll(reader.Payload)
	}
	if err == nil {
		t.Fatal("tampered object unexpectedly succeeded")
	}
}

func TestTruncatedObjectFails(t *testing.T) {
	salt, _ := RandomSalt()
	key, _ := DeriveKey("correct", salt, DefaultKDFParams())
	header, _ := NewHeader("task", "object", "data", EncodeSalt(salt))
	var encrypted bytes.Buffer
	_, _ = EncryptObject(&encrypted, key, header, map[string]string{"type": "test"}, bytes.NewReader(bytes.Repeat([]byte{1}, 4096)))
	truncated := encrypted.Bytes()[:encrypted.Len()-20]
	reader, err := OpenObject(bytes.NewReader(truncated), "correct")
	if err == nil {
		_, err = io.ReadAll(reader.Payload)
	}
	if err == nil {
		t.Fatal("truncated object unexpectedly succeeded")
	}
}
