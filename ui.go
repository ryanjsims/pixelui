package pixelui

import (
	"C"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/gopxl/pixel/v2"
)
import (
	"image/color"
	"runtime"
	"time"
	"unsafe"

	"github.com/gopxl/mainthread/v2"
	"github.com/gopxl/pixel/v2/backends/opengl"
	"github.com/gopxl/pixel/v2/ext/atlas"
)

const uiShader = `
#version 330 core
in vec4  vColor;
in vec2  vTexCoords;
in float vIntensity;
in vec4  vClipRect;

out vec4 fragColor;

uniform vec4 uColorMask;
uniform vec4 uTexBounds;
uniform sampler2D uTexture;
uniform vec4 uClipRect;

void main() {
	if ((vClipRect != vec4(0,0,0,0)) && (gl_FragCoord.x < vClipRect.x || gl_FragCoord.y < vClipRect.y || gl_FragCoord.x > vClipRect.z || gl_FragCoord.y > vClipRect.w))
		discard;
	fragColor = vColor;
	if (vIntensity == 0) {
		fragColor *= vColor * texture(uTexture, vTexCoords).a;
		fragColor *= uColorMask;
	} else {
		fragColor *= vColor * texture(uTexture, vTexCoords);
		fragColor *= uColorMask;
	}
}
`

// UI Stores the state of the pixelui UI
type UI struct {
	win        *opengl.Window
	context    *imgui.Context
	io         *imgui.IO
	platformIO *imgui.PlatformIO
	fonts      *imgui.FontAtlas
	timer      time.Time
	shader     *opengl.GLShader
	matrix     pixel.Matrix
	shaderTris *opengl.GLTriangles
	atlas      *atlas.Atlas
	group      atlas.Group
	font       atlas.TextureId
	cursors    map[imgui.MouseCursor]*opengl.Cursor
}

var CurrentUI *UI

// pixelui.NewUI flags:
//
//	NO_DEFAULT_FONT: Do not load the default font during New.
const (
	NO_DEFAULT_FONT uint8 = 1 << iota
)

// New Creates the UI and setups up its internal structures
func New(win *opengl.Window, atlas *atlas.Atlas, flags uint8) *UI {
	var context *imgui.Context
	mainthread.Call(func() {
		context = imgui.CreateContext()
	})

	ui := &UI{
		win:     win,
		context: context,
		atlas:   atlas,
		group:   atlas.MakeGroup(),
		cursors: make(map[imgui.MouseCursor]*opengl.Cursor),
	}
	CurrentUI = ui

	ui.io = imgui.CurrentIO()
	ui.platformIO = imgui.CurrentPlatformIO()
	ui.initIO()

	ui.fonts = ui.io.Fonts()

	ui.shader = opengl.NewGLShader(uiShader)

	ui.shaderTris = opengl.NewGLTriangles(ui.shader, pixel.MakeTrianglesData(0))

	if flags&NO_DEFAULT_FONT == 0 {
		ui.loadDefaultFont()
	}

	runtime.SetFinalizer(ui, (*UI).destroy)

	return ui
}

// Destroy cleans up the imgui context
func (ui *UI) destroy() {
	ui.context.InternalDestroy()
}

// NewFrame Call this at the beginning of the frame to tell the UI that the frame has started
func (ui *UI) NewFrame() {
	if !ui.timer.IsZero() {
		ui.io.SetDeltaTime(float32(time.Since(ui.timer).Seconds()))
	}
	ui.timer = time.Now()

	// imgui requires that io be set before calling NewFrame
	ui.prepareIO()

	imgui.NewFrame()
}

// update Handles general update type things and handle inputs. Called from ui.Draw.
func (ui *UI) update() {
}

func (ui *UI) updateMatrix() {
	ui.matrix = pixel.IM.ScaledXY(ui.win.Bounds().Center(), pixel.V(1, -1))
}

// Draw Draws the imgui UI to the Pixel Window
func (ui *UI) Draw(win *opengl.Window) {
	ui.updateMatrix()
	win.SetComposeMethod(pixel.ComposeOver)
	win.SetMatrix(ui.matrix)

	// imgui draws things from top-left as 0,0 where Pixel draws from bottom-left as 0,0,
	//	for drawing and handling inputs, we need to "flip" imgui.
	ui.update()

	// Tell imgui to render and get the resulting draw data
	imgui.Render()
	data := imgui.CurrentDrawData()

	// Since we have to redraw all of the triangles every frame,
	//	only resize the triangles list when we need to, and truncate
	//	it right before we draw (to get rid of any extra triangles).
	totalTris := 0

	// In each command, there is a vertex buffer that holds all of the vertices to draw;
	// 	there's also an index buffer which stores the indices into the vertex buffer that should
	//	be draw together. The vertex buffer is shared between multiple commands.
	vertexSize, posOffset, uvOffset, colOffset := imgui.VertexBufferLayout()
	indexSize := imgui.IndexBufferLayout()
	for _, cmds := range data.CommandLists() {
		var indexBufferOffset uintptr
		start, _ := cmds.GetVertexBuffer()
		idxStart, _ := cmds.GetIndexBuffer()

		for _, cmd := range cmds.Commands() {
			if cmd.HasUserCallback() {
				cmd.CallUserCallback(cmds)
			} else {
				count := cmd.ElemCount()
				iStart := totalTris
				totalTris += int(count)

				if ui.shaderTris.Len() < totalTris {
					ui.shaderTris.SetLen(totalTris)
				}

				clipRect := imguiRectToPixelRect(cmd.ClipRect()).Norm()
				clipRect.Min = ui.matrix.Project(clipRect.Min)
				clipRect.Max = ui.matrix.Project(clipRect.Max)
				clipRect = clipRect.Norm()

				id := uint32(cmd.TexID())
				spr := ui.atlas.Get(id)
				texRect := spr.Frame()

				intensity := 0.0
				if id != ui.font.ID() {
					intensity = 1.0
				}

				for i := 0; i < int(count); i++ {
					idx := unsafe.Pointer(uintptr(idxStart) + indexBufferOffset)
					index := *(*uint16)(idx)
					ptr := unsafe.Pointer(uintptr(start) + (uintptr(int(index) * vertexSize)))
					pos := *(*imgui.Vec2)(unsafe.Pointer(uintptr(ptr) + uintptr(posOffset)))
					uv := *(*imgui.Vec2)(unsafe.Pointer(uintptr(ptr) + uintptr(uvOffset)))
					col := *(*uint32)(unsafe.Pointer(uintptr(ptr) + uintptr(colOffset)))

					position := PV(pos)
					color := imguiColorToPixelColor(col)
					uuvv := ui.calcData(texRect, PV(uv))

					ui.shaderTris.SetPosition(iStart+i, position)
					ui.shaderTris.SetPicture(iStart+i, uuvv, intensity)
					ui.shaderTris.SetColor(iStart+i, pixel.ToRGBA(color))
					ui.shaderTris.SetClipRect(iStart+i, clipRect)
					indexBufferOffset += uintptr(indexSize)
				}
			}
		}
	}

	ui.shaderTris.SetLen(totalTris)
	ui.shaderTris.CopyVertices()
	win.MakePicture(ui.atlas.Textures()[0]).Draw(win.MakeTriangles(ui.shaderTris))

	win.SetMatrix(pixel.IM)
}

// recip returns the reciprocal of the given number.
func recip(m float64) float64 {
	return 1 / m
}

// calcData scales the incoming sprite uv to the proper sub-sprite in the packed atlas.
func (ui *UI) calcData(frame pixel.Rect, uuvv pixel.Vec) (pic pixel.Vec) {
	return uuvv.ScaledXY(frame.Size()).Add(frame.Min).ScaledXY(ui.atlas.Textures()[0].Bounds().Size().Map(recip))
}

// imguiColorToPixelColor Converts the imgui color to a Pixel color.
func imguiColorToPixelColor(c uint32) color.RGBA {
	// ABGR -> RGBA
	return color.RGBA{
		A: uint8((c >> 24) & 0xFF),
		B: uint8((c >> 16) & 0xFF),
		G: uint8((c >> 8) & 0xFF),
		R: uint8(c & 0xFF),
	}
}
