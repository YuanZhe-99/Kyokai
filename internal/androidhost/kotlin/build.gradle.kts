// The Kotlin of the native module androidhost: an Android library, which mygo
// build adds to the app's Gradle build, carrying libkyokai_core.so in its
// src/main/jniLibs/<abi>/ and Jetpack Window for foldable hinge bounds.
plugins {
    id("com.android.library")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.github.yuanzhe_99.kyokai.internal.androidhost"
    packaging { jniLibs { useLegacyPackaging = false } }
    defaultConfig {
        // Keep only the ABIs Go was built for (android/arm64 or android/amd64).
        providers.gradleProperty("mygoAbis").orNull?.split(',')?.filter { it.isNotEmpty() }?.let { abis ->
            ndk.abiFilters.addAll(abis)
        }
    }
    sourceSets {
        getByName("main") {
            jniLibs.setSrcDirs(emptyList<File>())
        }
    }
}

// Stage only the ABI(s) requested by mygoAbis into a clean jniLibs directory so
// an arm64 release APK never accidentally bundles x86_64 or vice versa.
val stageJniLibs = tasks.register<Sync>("stageKyokaiJniLibs") {
    val abis = providers.gradleProperty("mygoAbis").orNull?.split(',')?.filter { it.isNotEmpty() }
    from("src/main/jniLibs") {
        if (!abis.isNullOrEmpty()) {
            abis.forEach { include("$it/**") }
        }
    }
    into(layout.buildDirectory.dir("stagedJniLibs"))
}

android.sourceSets.getByName("main").jniLibs.srcDir(stageJniLibs.map { it.destinationDir })

tasks.matching { it.name.contains("JniLibFolders") }.configureEach {
    dependsOn(stageJniLibs)
}

dependencies {
    implementation("androidx.window:window:1.4.0")
}
