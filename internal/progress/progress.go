// Package progress menyediakan baris progres dinamis (ala pip/apt) berbasis
// carriage return, tanpa dependensi eksternal.
package progress

import (
	"fmt"
	"strings"
	"time"
)

// Hook dipanggil setiap pesan progres (dari Notify/Printf). Digunakan
// server REST untuk meng-update field Progress pada Job (mis. via polling).
var Hook func(string)

// Notify mengirim pesan progres ke hook bila di-set.
func Notify(msg string) {
	if Hook != nil {
		Hook(msg)
	}
}

// Bar merender baris progres dinamis (satu baris, ditimpa dengan \r).
// Lebar bar tetap `width` karakter (default 30 bila <= 0).
func Bar(current, total, width int) string {
	if total <= 0 {
		return ""
	}
	if width <= 0 {
		width = 30
	}
	pct := float64(current) / float64(total)
	if pct > 1 {
		pct = 1
	}
	filled := int(pct * float64(width))
	if filled > width {
		filled = width
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return fmt.Sprintf("\r  [%s] %5.1f%%  %d/%d", bar, pct*100, current, total)
}

// Counter merender baris progres dinamis TANPA total yang diketahui.
// Dipakai untuk tahap (seperti harvest dari server OAI yang tidak mengirim
// completeListSize) di mana kita tidak bisa hitung total secara presisi.
// Menampilkan: halaman, jumlah record, laju rata-rata, dan elapsed.
func Counter(pages, records int, elapsed time.Duration) string {
	rate := float64(0)
	if elapsed > 0 {
		rate = float64(records) / elapsed.Seconds()
	}
	return fmt.Sprintf("\r  halaman %-5d  record %-7d  %6.2f rec/dtk  %s",
		pages, records, rate, elapsed.Round(time.Second))
}
