package cli

import "awesomeProject/internal/progress"

// progressBar merender baris progres dinamis (satu baris, ditimpa dengan \r).
func progressBar(current, total, width int) string {
	return progress.Bar(current, total, width)
}
