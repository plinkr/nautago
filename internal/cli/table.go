package cli

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

func readLine(in io.Reader) (string, error) {
	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// Los anchos se miden en runas para que los valores con acentos queden alineados.
func renderTable(headers []string, rows [][]string) string {
	widths := make([]int, len(headers))
	measure := func(row []string) {
		for i, cell := range row {
			if i >= len(widths) {
				continue
			}
			if width := len([]rune(cell)); width > widths[i] {
				widths[i] = width
			}
		}
	}
	measure(headers)
	for _, row := range rows {
		measure(row)
	}

	rule := ruleFor(widths)
	table := []string{formatRow(headers, widths), rule}
	for _, row := range rows {
		table = append(table, formatRow(row, widths), rule)
	}
	return strings.Join(table, "\n")
}

func ruleFor(widths []int) string {
	var sb strings.Builder
	sb.WriteString("+")
	for _, width := range widths {
		sb.WriteString(strings.Repeat("-", width+2))
		sb.WriteString("+")
	}
	return sb.String()
}

func formatRow(row []string, widths []int) string {
	var sb strings.Builder
	sb.WriteString("|")
	for i, width := range widths {
		cell := ""
		if i < len(row) {
			cell = row[i]
		}
		fmt.Fprintf(&sb, " %s |", pad(cell, width))
	}
	return sb.String()
}

func pad(value string, width int) string {
	if missing := width - len([]rune(value)); missing > 0 {
		return value + strings.Repeat(" ", missing)
	}
	return value
}
