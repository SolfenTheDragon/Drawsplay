plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

fun Project.propOrEnv(name: String): String? {
    val prop = findProperty(name)?.toString()?.trim()
    if (!prop.isNullOrEmpty()) return prop
    val env = System.getenv(name)?.trim()
    return if (env.isNullOrEmpty()) null else env
}

// Release builds take their version from the release tag (DRAWSPLAY_VERSION,
// e.g. v1.4.0), so each release installs as an update of the last one:
// versionName 1.4.0, versionCode 100 + major*1000000 + minor*1000 + patch
// (above the fixed code 2 that releases before this used). Local builds keep
// versionCode 2 and report versionName "dev", which the in-app updater skips.
val releaseVersion = Regex("""^v(\d{1,3})\.(\d{1,3})\.(\d{1,3})$""")
    .matchEntire(project.propOrEnv("DRAWSPLAY_VERSION") ?: "")
    ?.destructured?.let { (major, minor, patch) -> Triple(major.toInt(), minor.toInt(), patch.toInt()) }

android {
    namespace = "com.drawsplay.tablet"
    compileSdk = 36

    defaultConfig {
        applicationId = "com.drawsplay.tablet"
        minSdk = 26
        targetSdk = 36
        versionCode = releaseVersion?.let { (major, minor, patch) -> 100 + major * 1_000_000 + minor * 1_000 + patch } ?: 2
        versionName = releaseVersion?.let { (major, minor, patch) -> "$major.$minor.$patch" } ?: "dev"
    }

    val releaseKeystoreFile = project.propOrEnv("DRAWSPLAY_ANDROID_KEYSTORE_FILE")
    val releaseStorePassword = project.propOrEnv("DRAWSPLAY_ANDROID_KEYSTORE_PASSWORD")
    val releaseKeyAlias = project.propOrEnv("DRAWSPLAY_ANDROID_KEY_ALIAS")
    val releaseKeyPassword = project.propOrEnv("DRAWSPLAY_ANDROID_KEY_PASSWORD")
    val hasReleaseSigning =
        !releaseKeystoreFile.isNullOrEmpty() &&
            !releaseStorePassword.isNullOrEmpty() &&
            !releaseKeyAlias.isNullOrEmpty() &&
            !releaseKeyPassword.isNullOrEmpty()

    signingConfigs {
        create("release") {
            if (hasReleaseSigning) {
                storeFile = file(releaseKeystoreFile!!)
                storePassword = releaseStorePassword
                keyAlias = releaseKeyAlias
                keyPassword = releaseKeyPassword
                enableV1Signing = true
                enableV2Signing = true
            }
        }
    }

    buildTypes {
        create("wifiTest") {
            initWith(getByName("debug"))
            applicationIdSuffix = ".wifitest"
            versionNameSuffix = "-wifi-test"
            matchingFallbacks += listOf("debug")
        }
        release {
            // Production release signing is provided by keystore secrets in CI.
            // Local dev builds without those secrets still fall back to the debug
            // keystore so sideload install behavior remains unchanged.
            signingConfig = if (hasReleaseSigning) {
                signingConfigs.getByName("release")
            } else {
                signingConfigs.getByName("debug")
            }
            isMinifyEnabled = false
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro"
            )
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    val requireSigning = setOf("1", "true", "yes").contains(
        (project.propOrEnv("DRAWSPLAY_REQUIRE_SIGNING") ?: "").lowercase()
    )
    if (requireSigning && !hasReleaseSigning) {
        val missing = mutableListOf<String>()
        if (releaseKeystoreFile.isNullOrEmpty()) missing += "DRAWSPLAY_ANDROID_KEYSTORE_FILE"
        if (releaseStorePassword.isNullOrEmpty()) missing += "DRAWSPLAY_ANDROID_KEYSTORE_PASSWORD"
        if (releaseKeyAlias.isNullOrEmpty()) missing += "DRAWSPLAY_ANDROID_KEY_ALIAS"
        if (releaseKeyPassword.isNullOrEmpty()) missing += "DRAWSPLAY_ANDROID_KEY_PASSWORD"
        if (missing.isNotEmpty()) {
            throw GradleException("Release signing is required; missing Android signing settings: ${missing.joinToString(", ")}")
        }
    }

    kotlinOptions {
        jvmTarget = "17"
    }
}

dependencies {
    implementation("org.bouncycastle:bcprov-jdk18on:1.86")
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("com.google.android.material:material:1.12.0")
    testImplementation("junit:junit:4.13.2")
}

tasks.withType<Test>().configureEach {
    val pairingPeer = System.getenv("DRAWSPLAY_PAIRING_TEST_PEER")
    if (!pairingPeer.isNullOrEmpty()) {
        inputs.file(pairingPeer).withPropertyName("wifiPairingTestPeer")
    }
}
