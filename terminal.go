package midterm

import (
	"io"
	"sync"

	"github.com/danielgatis/go-ansicode"
)

// Terminal represents a raw terminal capable of handling VT100 and VT102 ANSI
// escape sequences, some of which are handled by forwarding them to a local or
// remote session (e.g. OSC52 copy/paste).
type Terminal struct {
	// Screen is the current screen, embedded so that Terminal is a pass-through.
	*Screen

	// The title of the terminal
	Title string

	// Alt is either the alternate screen (if !IsAlt) or the main screen (if
	// IsAlt).
	Alt *Screen

	// IsAlt indicates whether the alt screen is active.
	IsAlt bool

	// AutoResizeY indicates whether the terminal should automatically resize
	// when the content exceeds its maximum height.
	AutoResizeY bool

	// AutoResizeX indicates that the terminal has no defined width - instead,
	// columns are dynamically allocated as text is printed, and not all rows
	// will be the same width.
	AutoResizeX bool

	// ForwardRequests is the writer to which we send requests to forward
	// to the terminal.
	ForwardRequests io.Writer

	// ForwardResponses is the writer to which we send responses to CSI/OSC queries.
	ForwardResponses io.Writer

	// Enable "raw" mode. Line endings do not imply a carriage return.
	Raw bool

	// AppendOnly instructs the terminal to not respect sequences that might
	// cause output to be lost - for example, setting a scrolling region.
	AppendOnly bool

	// wrap indicates that we've reached the end of the screen and need to wrap
	// to the next line if another character is printed.
	wrap bool

	*ansicode.Decoder

	// onResize is a hook called every time the terminal resizes.
	onResize OnResizeFunc

	// onScrollack is a hook called every time a line is about to be pushed out
	// of the visible screen region.
	onScrollback OnScrollbackFunc

	// for synchronizing e.g. writes and async resizing
	mut sync.Mutex
}

// Cursor represents both the position and text type of the cursor.
type Cursor struct {
	// Y and X are the coordinates.
	Y, X int

	// F is the format that will be displayed.
	F Format

	// S is the cursor style.
	S ansicode.CursorStyle
}

// ScrollRegion represents a region of the terminal that is
// scrollable.
type ScrollRegion struct {
	Start, End int
}

// NewAutoResizingTerminal creates a new Terminal object with small initial
// dimensions, configured to automatically resize width and height as needed.
//
// This may be useful for applications that want to display dynamically sized
// content.
func NewAutoResizingTerminal() *Terminal {
	term := NewTerminal(0, 0)
	term.AutoResizeX = true
	term.AutoResizeY = true
	return term
}

// NewTerminal creates a new Terminal object with the specified dimensions. y
// and x must both be greater than zero.
//
// Each cell is set to contain a ' ' rune, and all formats are left as the
// default.
func NewTerminal(rows, cols int) *Terminal {
	v := &Terminal{
		Screen: newScreen(rows, cols),
	}
	v.Decoder = ansicode.NewDecoder(v)
	v.reset()
	return v
}

// Write writes the input sequence to the terminal.
func (v *Terminal) Write(p []byte) (int, error) {
	if trace != nil {
		trace.Write(p)
	}
	return v.Decoder.Write(p)
}

func (v *Terminal) Reset() {
	v.mut.Lock()
	defer v.mut.Unlock()
	v.reset()
}

func (v *Terminal) UsedHeight() int {
	v.mut.Lock()
	defer v.mut.Unlock()
	return v.MaxY + 1
}

func (v *Terminal) UsedWidth() int {
	v.mut.Lock()
	defer v.mut.Unlock()
	return v.MaxX + 1
}

// Resize sets the terminal height and width to rows and cols and disables
// auto-resizing on both axes.
func (v *Terminal) Resize(rows, cols int) {
	v.mut.Lock()
	v.resize(rows, cols)

	// disable auto-resize upon manually resizing. what's the point if the new
	// size won't be respected?
	v.AutoResizeX = false
	v.AutoResizeY = false

	f := v.onResize
	v.mut.Unlock()
	if f != nil {
		f(rows, cols)
	}
}

// Resize sets the terminal width to cols and disables auto-resizing width.
func (v *Terminal) ResizeX(cols int) {
	v.mut.Lock()
	v.resize(v.Height, cols)

	// disable auto-resize upon manually resizing. what's the point if the new
	// size won't be respected?
	v.AutoResizeX = false

	f := v.onResize
	v.mut.Unlock()
	if f != nil {
		f(v.Height, cols)
	}
}

// Resize sets the terminal height to rows rows and disables auto-resizing
// height.
func (v *Terminal) ResizeY(rows int) {
	v.mut.Lock()
	v.resize(rows, v.Width)

	// disable auto-resize upon manually resizing. what's the point if the new
	// size won't be respected?
	v.AutoResizeY = false

	f := v.onResize
	v.mut.Unlock()
	if f != nil {
		f(rows, v.Width)
	}
}

type OnScrollbackFunc func(line Line)

// OnScrollback sets a hook that is called every time a line is about to be
// pushed out of the visible screen region.
func (v *Terminal) OnScrollback(f OnScrollbackFunc) {
	v.mut.Lock()
	v.onScrollback = f
	v.mut.Unlock()
}

type OnResizeFunc func(rows, cols int)

// OnResize sets a hook that is called every time the terminal resizes.
func (v *Terminal) OnResize(f OnResizeFunc) {
	f(v.Height, v.Width)
	v.mut.Lock()
	v.onResize = f
	v.mut.Unlock()
}

func (v *Terminal) resize(h, w int) {
	v.Screen.resize(h, w)
	if v.Alt != nil {
		v.Alt.resize(h, w)
	}
}

// put puts r onto the current cursor's position, then advances the cursor.
func (v *Terminal) put(r rune) {
	if v.wrap {
		v.Cursor.X = 0
		v.moveDown()
		v.wrap = false
	}
	x, y, f := v.Cursor.X, v.Cursor.Y, v.Cursor.F
	v.paint(y, x, f, r)
	if y > v.MaxY {
		v.MaxY = y
	}
	if x > v.MaxX {
		v.MaxX = x
	}
	v.advance()
}

// advance advances the cursor, wrapping to the next line if need be.
func (v *Terminal) advance() {
	if !v.AutoResizeX && v.Cursor.X == v.Width-1 {
		v.wrap = true
	} else {
		v.moveRel(0, 1)
		v.changed(v.Cursor.Y, true)
	}
}

func (v *Terminal) resizeY(h int) {
	v.Screen.resizeY(h)
	if v.Alt != nil {
		v.Alt.resizeY(h)
	}
}

func (v *Terminal) swapAlt() {
	v.IsAlt = !v.IsAlt
	v.Screen, v.Alt = v.Alt, v.Screen
}

func scrollUp[T any](arr [][]T, positions, start, end int, empty T) {
	if start < 0 || end > len(arr) || start >= end || positions <= 0 {
		return // handle invalid inputs
	}

	// for i := start; i < end-positions; i++ {
	for i := start; i < (end+1)-positions; i++ { // +1 fixes weird stuff when shell scrollback exceeds window height
		arr[i] = make([]T, len(arr[i+positions]))
		copy(arr[i], arr[i+positions])
	}

	// Fill the newly scrolled lines with blank runes
	for i := end - positions + 1; i <= end; i++ { // +1 and <= fixes last line not being cleared
		arr[i] = make([]T, len(arr[i]))
		for j := range arr[i] {
			arr[i][j] = empty
		}
	}
}

func scrollUpShallow[T any](arr []T, positions, start, end int, init func() T) {
	if start < 0 || end > len(arr) || start >= end || positions <= 0 {
		return // handle invalid inputs
	}

	// for i := start; i < end-positions; i++ {
	for i := start; i < (end+1)-positions; i++ { // +1 fixes weird stuff when shell scrollback exceeds window height
		arr[i] = arr[i+positions]
	}

	// Fill the newly scrolled lines with blank runes
	for i := end - positions + 1; i <= end; i++ { // +1 and <= fixes last line not being cleared
		arr[i] = init()
	}
}

func scrollDown[T any](arr [][]T, positions, start, end int, empty T) {
	if start < 0 || end > len(arr) || start >= end || positions <= 0 {
		return // handle invalid inputs
	}

	// Shift all rows down
	for i := end; i >= start+positions; i-- {
		arr[i] = make([]T, len(arr[i-positions]))
		copy(arr[i], arr[i-positions])
	}

	// Fill the newly scrolled lines with blank runes
	for i := start; i < start+positions; i++ {
		arr[i] = make([]T, len(arr[i]))
		for j := range arr[i] {
			arr[i][j] = empty
		}
	}
}

func scrollDownShallow[T any](arr []T, positions, start, end int, init func() T) {
	if start < 0 || end > len(arr) || start >= end || positions <= 0 {
		return // handle invalid inputs
	}

	// Shift all rows down
	for i := end; i >= start+positions; i-- {
		arr[i] = arr[i-positions]
	}

	// Fill the newly scrolled lines with blank runes
	for i := start; i < start+positions; i++ {
		arr[i] = init()
	}
}

func insertLines[T any](arr [][]T, start, ps, scrollStart, scrollEnd int, empty T) {
	if start < 0 || start+ps > len(arr) || ps <= 0 {
		return // handle invalid inputs
	}

	// Shift lines down by Ps positions starting from the start position
	for i := scrollEnd; i >= start+ps; i-- {
		arr[i] = arr[i-ps]
	}

	// Fill the newly inserted lines with the empty value
	for i := start; i < start+ps; i++ {
		arr[i] = make([]T, len(arr[i]))
		for j := range arr[i] {
			arr[i][j] = empty
		}
	}
}

func insertLinesShallow[T any](arr []T, start, ps, scrollStart, scrollEnd int, init func() T) {
	if start < 0 || start+ps > len(arr) || ps <= 0 {
		return // handle invalid inputs
	}

	// Shift lines down by Ps positions starting from the start position
	for i := scrollEnd; i >= start+ps; i-- {
		arr[i] = arr[i-ps]
	}

	// Fill the newly inserted lines with the empty value
	for i := start; i < start+ps; i++ {
		arr[i] = init()
	}
}

func deleteLines[T any](arr [][]T, start, ps, scrollStart, scrollEnd int, empty T) {
	if start < 0 || start+ps > len(arr) || ps <= 0 {
		return // handle invalid inputs
	}

	// Delete Ps lines starting from the start position
	copy(
		arr[start:scrollEnd],
		arr[start+ps:],
	)

	// Fill the end lines with the empty value
	fillStart := scrollEnd - ps
	for i := fillStart + 1; i < scrollEnd+1; i++ {
		arr[i] = make([]T, len(arr[start])) // Assume all lines have the same length as the start line
		for j := range arr[i] {
			arr[i][j] = empty
		}
	}
}

func deleteLinesShallow[T any](arr []T, start, ps, scrollStart, scrollEnd int, init func() T) {
	if start < 0 || start+ps > len(arr) || ps <= 0 {
		return // handle invalid inputs
	}

	// Delete Ps lines starting from the start position
	copy(
		arr[start:scrollEnd],
		arr[start+ps:],
	)

	// Fill the end lines with the empty value
	fillStart := scrollEnd - ps
	for i := fillStart + 1; i < scrollEnd+1; i++ {
		arr[i] = init()
	}
}

func eraseCharacters[T any](arr [][]T, row, col, ps int, empty T) {
	if row < 0 || row >= len(arr) || col < 0 || col+ps > len(arr[row]) {
		return // handle invalid inputs
	}

	if ps <= 0 {
		ps = 1 // if Ps is 0 or negative, erase one character
	}

	// Replace Ps characters with the empty value starting from the given position
	for i := col; i < col+ps; i++ {
		arr[row][i] = empty
	}
}

func deleteCharacters[T any](arr [][]T, row, col, ps int, empty T) {
	if row < 0 || row >= len(arr) || col < 0 || col >= len(arr[row]) || ps < 0 {
		return // handle invalid inputs
	}

	// Calculate the actual number of characters to delete, so it doesn't exceed the available space
	actualPs := ps
	if actualPs == 0 {
		actualPs = 1 // if Ps is 0, delete one character
	}
	if col+actualPs > len(arr[row]) {
		actualPs = len(arr[row]) - col
	}

	// Shift characters to the left by Ps positions starting from the given column
	copy(arr[row][col:], arr[row][col+actualPs:])

	// Fill the end characters with the empty value
	for i := len(arr[row]) - ps; i < len(arr[row]); i++ {
		arr[row][i] = empty
	}
}

func insertEmpties[T any](arr [][]T, row, col, ps int, empty T) {
	if row < 0 || row >= len(arr) || col < 0 || col > len(arr[row]) || ps <= 0 {
		return // Return the original array if the inputs are out of bounds or invalid
	}

	// Create a slice with ps empty elements
	empties := make([]T, ps)
	for i := range empties {
		empties[i] = empty
	}

	// Insert the empties at the specified row and column
	inserted := append(arr[row][:col], append(empties, arr[row][col:]...)...)

	// clip the row to the length of the original array
	//
	// NB: we don't actually need to handle wrapping. sh for example handles that
	// automatically, by manually writing the next row and moving the cursor back
	// up
	arr[row] = inserted[:len(arr[row])]
}

func (v *Terminal) insertCharacters(n int) {
	insertEmpties(v.Content, v.Cursor.Y, v.Cursor.X, n, ' ')
	v.Format.Insert(v.Cursor.Y, v.Cursor.X, v.Cursor.F, n)
	v.changed(v.Cursor.Y, false)
}

func (v *Terminal) deleteCharacters(n int) {
	v.wrap = false // delete characters resets the wrap state.
	deleteCharacters(v.Content, v.Cursor.Y, v.Cursor.X, n, ' ')
	v.Format.Delete(v.Cursor.Y, v.Cursor.X, n)
	v.changed(v.Cursor.Y, false)
}

func (v *Terminal) eraseCharacters(n int) {
	v.wrap = false // erase characters resets the wrap state.
	eraseCharacters(v.Content, v.Cursor.Y, v.Cursor.X, n, ' ')
	for i := 0; i < n; i++ {
		v.Format.Paint(v.Cursor.Y, v.Cursor.X+i, v.Cursor.F)
	}
	v.changed(v.Cursor.Y, false)
}

func (v *Terminal) insertLines(n int) {
	v.ensureHeight(v.Cursor.Y + n)
	start, end := v.scrollRegion()
	if v.Cursor.Y < start || v.Cursor.Y > end {
		return
	}
	v.wrap = false
	insertLines(v.Content, v.Cursor.Y, n, start, end, ' ')
	insertLinesShallow(v.Format.Rows, v.Cursor.Y, n, start, end, func() *Region {
		return &Region{Size: v.Width, F: v.Cursor.F}
	})
	insertLinesShallow(v.Changes, v.Cursor.Y, n, start, end, func() uint64 {
		return 1
	})
}

func (v *Terminal) deleteLines(n int) {
	start, end := v.scrollRegion()
	if v.Cursor.Y < start || v.Cursor.Y > end {
		return
	}
	v.wrap = false // delete lines resets the wrap state.
	deleteLines(v.Content, v.Cursor.Y, n, start, end, ' ')
	deleteLinesShallow(v.Format.Rows, v.Cursor.Y, n, start, end, func() *Region {
		return &Region{Size: v.Width, F: v.Cursor.F}
	})
	deleteLinesShallow(v.Changes, v.Cursor.Y, n, start, end, func() uint64 {
		return 1
	})
}

func (v *Terminal) scrollDownN(n int) {
	v.wrap = false // scroll down resets the wrap state.
	start, end := v.scrollRegion()
	scrollDown(v.Content, n, start, end, ' ')
	scrollDownShallow(v.Format.Rows, n, start, end, func() *Region {
		return &Region{Size: v.Width, F: v.Cursor.F}
	})
	scrollDownShallow(v.Changes, n, start, end, func() uint64 {
		return 1
	})
}

func (v *Terminal) scrollUpN(n int) {
	if v.onScrollback != nil {
		for i := 0; i < n; i++ {
			// v.onScrollback(Line{v.Content[i], v.Format[i]})
		}
	}
	// v.wrap = false // scroll up does NOT reset the wrap state.
	start, end := v.scrollRegion()
	scrollUp(v.Content, n, start, end, ' ')
	scrollUpShallow(v.Format.Rows, n, start, end, func() *Region {
		return &Region{Size: v.Width, F: v.Cursor.F}
	})
	scrollUpShallow(v.Changes, n, start, end, func() uint64 {
		return 1
	})
}

func (v *Terminal) scrollRegion() (int, int) {
	if v.ScrollRegion == nil {
		return 0, v.Height - 1
	} else {
		return v.ScrollRegion.Start, v.ScrollRegion.End
	}
}

func (v *Terminal) scrollOne() {
	v.scrollUpN(1)
	v.Cursor.Y = v.Height - 1
}

func (v *Terminal) home(y, x int) {
	v.wrap = false // cursor movement always resets the wrap state.
	v.moveAbs(y, x)
}

// eraseDirection is the logical direction in which an erase command happens,
// from the cursor. For both erase commands, forward is 0, backward is 1,
// and everything is 2.
type eraseDirection int

const (
	// From the cursor to the end, inclusive.
	eraseForward eraseDirection = iota

	// From the beginning to the cursor, inclusive.
	eraseBack

	// Everything.
	eraseAll
)

func (v *Terminal) eraseRegion(y1, x1, y2, x2 int) {
	// Erasing lines and columns clears the wrap state.
	v.wrap = false
	if y1 > y2 && y2 > 0 {
		y1, y2 = y2, y1
	}
	if x1 > x2 && x2 > 0 {
		x1, x2 = x2, x1
	}
	f := v.Cursor.F
	for y := y1; y <= y2; y++ {
		if len(v.Content) <= y {
			continue
		}
		rowX2 := x2
		if rowX2 < 0 {
			// to handle dynamic width, assume "clear to end"
			rowX2 = len(v.Content[y]) - 1
		}
		for x := x1; x <= rowX2; x++ {
			v.clear(y, x, f)
		}
	}
}

func (v *Terminal) moveDown() {
	if v.ScrollRegion != nil && v.Cursor.Y == v.ScrollRegion.End {
		// if we're at the bottom of the scroll region, scroll it instead of
		// moving the cursor, so the cursor stays at the bottom of the scroll
		// region
		v.scrollUpN(1)
	} else if v.Cursor.Y == v.Height-1 && !v.AutoResizeY {
		// if we're at the bottom of the entire screen, and we're not
		// configured to auto-resize, scroll the screen
		v.scrollUpN(1)
	} else {
		v.moveRel(1, 0)
	}
}

func (v *Terminal) moveUp() {
	if v.Cursor.Y == 0 || v.ScrollRegion != nil && v.Cursor.Y == v.ScrollRegion.Start {
		// if we're at the bottom of the scroll region, scroll it instead of
		// moving the cursor
		v.scrollDownN(1)
	} else {
		v.moveRel(-1, 0)
	}
}

func (v *Terminal) save() {
	v.SavedCursor = v.Cursor
}

func (v *Terminal) unsave() {
	v.Cursor = v.SavedCursor
}
