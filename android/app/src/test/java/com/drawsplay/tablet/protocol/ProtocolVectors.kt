package com.drawsplay.tablet.protocol

import java.io.File

/**
 * Reads the shared protocol vectors in testdata/protocols at the repository root, which the Go host
 * and dd-host test against too. One case per line: a name, then key=value pairs; "#" starts a comment.
 */
class ProtocolVector(val name: String, private val fields: Map<String, String>) {
    fun hex(key: String): ByteArray {
        val s = fields.getValue(key)
        if (s == "-") return ByteArray(0)
        return ByteArray(s.length / 2) { s.substring(it * 2, it * 2 + 2).toInt(16).toByte() }
    }

    fun long(key: String): Long = fields.getValue(key).toLong()
}

object ProtocolVectors {
    fun load(file: String): List<ProtocolVector> {
        var dir: File? = File(System.getProperty("user.dir")).absoluteFile
        while (dir != null && !File(dir, "testdata/protocols").isDirectory) dir = dir.parentFile
        requireNotNull(dir) { "testdata/protocols not found above ${System.getProperty("user.dir")}" }
        return File(dir, "testdata/protocols/$file").readLines()
            .map { it.trim() }
            .filter { it.isNotEmpty() && !it.startsWith("#") }
            .map { line ->
                val parts = line.split(Regex("\\s+"))
                ProtocolVector(parts[0], parts.drop(1).associate { it.substringBefore('=') to it.substringAfter('=') })
            }
            .also { require(it.isNotEmpty()) { "$file holds no vectors" } }
    }
}
