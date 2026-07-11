plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
}

android {
    namespace = "link.vitl.field"
    compileSdk = 35

    defaultConfig {
        applicationId = "link.vitl.field"
        // minSdk 21 matches the -androidapi 21 flag passed to `gomobile bind`
        minSdk = 21
        targetSdk = 35
        versionCode = 1
        versionName = "0.1.0"
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"))
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
}

dependencies {
    // Generated AAR from `mobile/build_android.sh`.
    // Re-run that script whenever mobile/vitallink/vitallink.go changes.
    // gomobile bind bundles the Java binding runtime inside the AAR itself —
    // no separate Maven artifact is needed.
    implementation(files("libs/vitallink.aar"))


    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.appcompat)
    implementation(libs.androidx.activity.ktx)
    implementation(libs.androidx.constraintlayout)
}
