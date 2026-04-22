package solanapay

import (
	"fmt"

	qrcode "github.com/skip2/go-qrcode"
)

// QRPNG renders url as a PNG-encoded QR code sized for mobile wallet
// scanning. Error correction level H tolerates up to ~30% occlusion,
// which matters for checkout pages that overlay a wallet logo or display
// the code under glare; the size cost (~30% larger image vs level M) is
// acceptable for a one-time payment flow.
func QRPNG(url string, size int) ([]byte, error) {
	if url == "" {
		return nil, fmt.Errorf("solanapay: empty URL")
	}
	if size <= 0 {
		return nil, fmt.Errorf("solanapay: qr size must be positive, got %d", size)
	}
	png, err := qrcode.Encode(url, qrcode.High, size)
	if err != nil {
		return nil, fmt.Errorf("solanapay: qr encode: %w", err)
	}
	return png, nil
}
