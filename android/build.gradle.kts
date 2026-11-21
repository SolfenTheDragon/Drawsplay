plugins {
    // AGP 9 compiles Kotlin itself (built-in Kotlin, KGP 2.2.10), so the
    // org.jetbrains.kotlin.android plugin is no longer applied. API 37
    // (Android 17) needs AGP 9.1.1 or later.
    id("com.android.application") version "9.5.2" apply false
}
