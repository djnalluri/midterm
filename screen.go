package midterm

type Screen struct {
	// Height and Width are the dimensions of the terminal.
	Height, Width int

	// Content is the text in the terminal.
	Content [][]rune

	// Format is the display properties of each cell.
	Format [][]Format

	// Cursor is the current state of the cursor.
	Cursor Cursor
}

func newScreen(h, w int) *Screen {
	s := &Screen{
		Height: h,
		Width:  w,
	}
	s.reset()
	return s
}

func (s *Screen) reset() {
	s.Content = make([][]rune, s.Height)
	s.Format = make([][]Format, s.Height)
	for row := 0; row < s.Height; row++ {
		s.Content[row] = make([]rune, s.Width)
		s.Format[row] = make([]Format, s.Width)
		for col := 0; col < s.Width; col++ {
			s.Content[row][col] = ' '
		}
	}
	s.Cursor.X = 0
	s.Cursor.Y = 0
}

func (v *Screen) resize(h, w int) {
	if h > v.Height {
		n := h - v.Height
		for row := 0; row < n; row++ {
			v.Content = append(v.Content, make([]rune, v.Width))
			v.Format = append(v.Format, make([]Format, v.Width))
			for col := 0; col < v.Width; col++ {
				v.clear(v.Height+row, col, Format{})
			}
		}
	} else if h < v.Height {
		v.Content = v.Content[:h]
		v.Format = v.Format[:h]
	}

	if w > v.Width {
		for i := range v.Content {
			row := make([]rune, w)
			copy(row, v.Content[i])
			v.Content[i] = row
			format := make([]Format, w)
			copy(format, v.Format[i])
			v.Format[i] = format
			for j := v.Width; j < w; j++ {
				v.clear(i, j, Format{})
			}
		}
	} else if w < v.Width {
		for i := range v.Content {
			v.Content[i] = v.Content[i][:w]
			v.Format[i] = v.Format[i][:w]
		}
	}
}

func (v *Screen) clear(y, x int, format Format) {
	if y >= len(v.Content) || x >= len(v.Content[0]) {
		return
	}
	v.Content[y][x] = ' '
	v.Format[y][x] = format
}