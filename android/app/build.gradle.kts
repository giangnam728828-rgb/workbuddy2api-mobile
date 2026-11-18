plugins {
    id("com.android.application")
}

// 签名配置从 gradle.properties 读取（仓库内自签，保证每次 CI 构建签名一致，
// 用户可以直接覆盖安装升级，不会出现「应用未安装 / 签名不一致」）。
val storeFilePath: String = (project.findProperty("WB2API_STORE_FILE") as String?) ?: "../keystore/wb2api.jks"
val storePasswordValue: String = (project.findProperty("WB2API_STORE_PASSWORD") as String?) ?: "wb2api2024"
val keyAliasValue: String = (project.findProperty("WB2API_KEY_ALIAS") as String?) ?: "wb2api"
val keyPasswordValue: String = (project.findProperty("WB2API_KEY_PASSWORD") as String?) ?: "wb2api2024"

android {
    namespace = "com.wb2api.panel"
    compileSdk = 34

    defaultConfig {
        applicationId = "com.wb2api.panel"
        minSdk = 24
        targetSdk = 34
        // versionCode 必须单调递增，否则部分 ROM 的安装器会拒绝覆盖安装
        // （同版本号覆盖在原生 Android 上允许，但国产 ROM 常按「不大于已装版本」直接拦）。
        // CI 用 11300 + run_number 注入，保证每次构建都大于上一版；本地默认 11301。
        versionCode = (project.findProperty("wb2apiVersionCode") as String?)?.toIntOrNull() ?: 11301
        versionName = (project.findProperty("wb2apiVersionName") as String?) ?: "1.13.1"

        // Go 交叉编译产物按 ABI 放进 jniLibs：arm64 覆盖 99% 真机，
        // armeabi-v7a 兜底老设备，x86_64 给模拟器/少数平板。
        ndk {
            abiFilters += listOf("arm64-v8a", "armeabi-v7a", "x86_64")
        }
    }

    signingConfigs {
        create("release") {
            storeFile = file(storeFilePath)
            storePassword = storePasswordValue
            keyAlias = keyAliasValue
            keyPassword = keyPasswordValue
            // V1 给 Android 7.x，V2 覆盖 8.0+，V3 让 9+ 的密钥轮换/校验更严谨。
            enableV1Signing = true
            enableV2Signing = true
            enableV3Signing = true
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            isShrinkResources = false
            signingConfig = signingConfigs.getByName("release")
        }
    }

    // 关键：Go 二进制作为 lib*.so 打进 jniLibs，必须让安装时解压到 nativeLibraryDir。
    // Android 10+ 禁止执行应用数据目录里的文件（W^X），但 nativeLibraryDir 可执行，
    // 这是「无 root 在 App 内跑自带服务」的唯一稳定路径。
    packaging {
        jniLibs {
            useLegacyPackaging = true
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    buildFeatures {
        buildConfig = true
    }

    lint {
        abortOnError = false
        checkReleaseBuilds = false
    }

    // 输出名固定，方便 CI 收集与用户辨认
    applicationVariants.all {
        outputs.all {
            (this as com.android.build.gradle.internal.api.BaseVariantOutputImpl).outputFileName =
                "WorkBuddy2API-${versionName}-${name}.apk"
        }
    }
}
