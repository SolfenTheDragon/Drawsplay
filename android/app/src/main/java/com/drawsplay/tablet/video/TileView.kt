package com.drawsplay.tablet.video

import android.content.Context
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Paint
import android.util.AttributeSet
import android.view.View

/**
 * Presents lossless tile updates over the video surface. One small bitmap per
 * tile means a delta only re-uploads the tiles that changed, instead of a
 * whole-screen texture. Tile data arrives on the video reader thread; drawing
 * happens on the UI thread.
 */
class TileView @JvmOverloads constructor(
    context: Context,
    attrs: AttributeSet? = null
) : View(context, attrs), TileDecoder.Sink {
    private class Grid(val width: Int, val height: Int, val tileSize: Int) {
        val cols = (width + tileSize - 1) / tileSize
        val rows = (height + tileSize - 1) / tileSize
        val tiles = arrayOfNulls<Bitmap>(cols * rows)
    }

    @Volatile
    private var grid: Grid? = null
    private val paint = Paint(Paint.FILTER_BITMAP_FLAG)

    override fun onFullBegin(width: Int, height: Int, tileSize: Int) {
        grid = Grid(width, height, tileSize)
    }

    override fun onTile(tx: Int, ty: Int, w: Int, h: Int, argb: IntArray) {
        val g = grid ?: return
        if (tx >= g.cols || ty >= g.rows) return
        val idx = ty * g.cols + tx
        var bmp = g.tiles[idx]
        if (bmp == null || bmp.width != w || bmp.height != h) {
            bmp = Bitmap.createBitmap(w, h, Bitmap.Config.ARGB_8888)
            g.tiles[idx] = bmp
        }
        bmp.setPixels(argb, 0, w, 0, 0, w, h)
    }

    override fun onFullEnd() {
        postInvalidateOnAnimation()
    }

    override fun onPacketDone() {
        postInvalidateOnAnimation()
    }

    fun clear() {
        grid = null
    }

    override fun onDraw(canvas: Canvas) {
        val g = grid ?: return
        canvas.save()
        canvas.scale(width.toFloat() / g.width, height.toFloat() / g.height)
        for (ty in 0 until g.rows) {
            for (tx in 0 until g.cols) {
                val bmp = g.tiles[ty * g.cols + tx] ?: continue
                canvas.drawBitmap(bmp, (tx * g.tileSize).toFloat(), (ty * g.tileSize).toFloat(), paint)
            }
        }
        canvas.restore()
    }
}
