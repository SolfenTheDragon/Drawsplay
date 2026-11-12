package com.drawsplay.tablet

import java.io.File
import javax.xml.parsers.DocumentBuilderFactory
import kotlin.math.pow
import org.junit.Assert.assertTrue
import org.junit.Test

class LocalUiContrastTest {
    @Test fun normalTextAndButtonStatesMeetContrastInBothSystemThemes() {
        val res = listOf(File("src/main/res"), File("app/src/main/res"),
            File("android/app/src/main/res")).first { it.isDirectory }
        val light = colors(File(res, "values/colors.xml"))
        for (palette in listOf(light, light + colors(File(res, "values-night/colors.xml")))) {
            for (background in listOf("ds_bg", "ds_panel", "ds_panel_raised")) {
                for (foreground in listOf("ds_text", "ds_muted", "ds_accent")) {
                    checkContrast(palette.getValue(foreground), palette.getValue(background),
                        "$foreground on $background")
                }
            }
            for (background in listOf("ds_accent_strong", "ds_accent_pressed")) {
                checkContrast("#FFFFFF", palette.getValue(background), "Primary button: $background")
            }
            checkContrast(palette.getValue("ds_disabled_text"), palette.getValue("ds_disabled"),
                "Disabled controls")
        }
    }

    private fun colors(file: File): Map<String, String> {
        val nodes = DocumentBuilderFactory.newInstance().newDocumentBuilder()
            .parse(file).getElementsByTagName("color")
        return (0 until nodes.length).associate { index ->
            val node = nodes.item(index)
            node.attributes.getNamedItem("name").nodeValue to node.textContent.trim()
        }
    }

    private fun checkContrast(foreground: String, background: String, label: String) {
        val a = luminance(foreground)
        val b = luminance(background)
        val ratio = (maxOf(a, b) + 0.05) / (minOf(a, b) + 0.05)
        assertTrue("$label: $foreground on $background = $ratio", ratio >= 4.5)
    }

    private fun luminance(hex: String): Double {
        val channels = listOf(1, 3, 5).map { offset ->
            val s = hex.substring(offset, offset + 2).toInt(16) / 255.0
            if (s <= 0.04045) s / 12.92 else ((s + 0.055) / 1.055).pow(2.4)
        }
        return channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722
    }
}
