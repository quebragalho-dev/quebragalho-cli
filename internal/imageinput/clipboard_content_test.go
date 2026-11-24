package imageinput

import (
	"errors"
	"reflect"
	"testing"
)

// A combinação imagem+arquivos respeita a prioridade: erro de imagem domina
// (a extração falhou sobre algo que existe); erro de arquivos só aparece sem
// imagem (no Linux, ferramenta ausente com clipboard vazio de imagem).
func TestReadClipboardContentMatrix(t *testing.T) {
	restore := func() func() {
		img, files := clipboardImageReader, clipboardFilesReader
		return func() { clipboardImageReader, clipboardFilesReader = img, files }
	}()
	t.Cleanup(restore)

	cases := []struct {
		name      string
		image     []byte
		mediaType string
		imageErr  error
		files     []string
		filesErr  error
		want      ClipboardContent
		wantErr   error
	}{
		{
			name: "imagem sozinha", image: []byte{1}, mediaType: "image/png",
			want: ClipboardContent{Image: []byte{1}, MediaType: "image/png"},
		},
		{
			name: "arquivos sozinhos", files: []string{"/a.png", "/b.pdf"},
			want: ClipboardContent{Files: []string{"/a.png", "/b.pdf"}},
		},
		{
			name: "imagem e arquivos", image: []byte{1}, mediaType: "image/png",
			files: []string{"/a.png"},
			want:  ClipboardContent{Image: []byte{1}, MediaType: "image/png", Files: []string{"/a.png"}},
		},
		{
			name: "vazio silencioso",
			want: ClipboardContent{},
		},
		{
			name:     "erro de imagem domina",
			imageErr: errors.New("boom"), files: []string{"/a.png"},
			wantErr: errors.New("boom"),
		},
		{
			name:     "ferramenta ausente sem imagem",
			filesErr: ErrClipboardToolMissing,
			wantErr:  ErrClipboardToolMissing,
		},
		{
			name:  "ferramenta ausente com imagem ignora arquivos",
			image: []byte{1}, mediaType: "image/png", filesErr: ErrClipboardToolMissing,
			want: ClipboardContent{Image: []byte{1}, MediaType: "image/png"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clipboardImageReader = func() ([]byte, string, error) {
				return tc.image, tc.mediaType, tc.imageErr
			}
			clipboardFilesReader = func() ([]string, error) {
				return tc.files, tc.filesErr
			}
			got, err := readClipboardContentSeparately()
			if tc.wantErr != nil {
				if err == nil || err.Error() != tc.wantErr.Error() {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want none", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
