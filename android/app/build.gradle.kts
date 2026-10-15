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

android {
    namespace = "com.drawsplay.tablet"
    compileSdk = 36

    defaultConfig {
        applicationId = "com.drawsplay.tablet"
        minSdk = 26
        targetSdk = 36
        versionCode = 2
        versionName = "0.2.0"
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
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("androidx.security:security-crypto:1.1.0-alpha06")
    implementation("com.google.android.material:material:1.12.0")
    testImplementation("junit:junit:4.13.2")
}
