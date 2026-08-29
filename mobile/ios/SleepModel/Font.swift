//
//  Font.swift
//  Sense
//
//  Created by Jimmy Lu on 1/24/17.
//  Copyright © 2017 Hello. All rights reserved.
//

import Foundation
import UIKit

@available(iOS 8.2, *)
@objcMembers class Font: NSObject {
    
    fileprivate static let resourceName = "fonts"
    
    fileprivate static let systemFontName = "style.font.SYSTEM"
    fileprivate static let weightThin = "style.font.THIN"
    fileprivate static let weightLight = "style.font.LIGHT"
    fileprivate static let weightUltraLight = "style.font.ULTRALIGHT"
    fileprivate static let weightRegular = "style.font.REGULAR"
    fileprivate static let weightMedium = "style.font.MEDIUM"
    
    fileprivate static let keyName = "style.font.name"
    fileprivate static let keySize = "style.font.size"
    fileprivate static let keyWeight = "style.font.weight"
    
    fileprivate static var resource: [String: Any]! {
        return Bundle.read(jsonFileName: resourceName) as? [String: Any]
    }
    
    fileprivate static func weight(string: String?) -> UIFont.Weight {
        guard let weightText = string else {
            return UIFont.Weight.regular
        }
        
        switch weightText {
        case Font.weightThin:
            return UIFont.Weight.thin
        case Font.weightLight:
            return UIFont.Weight.light
        case Font.weightMedium:
            return UIFont.Weight.medium
        case Font.weightUltraLight:
            return UIFont.Weight.ultraLight
        case Font.weightRegular:
            fallthrough
        default:
            return UIFont.Weight.regular
        }
    }
    
    // MARK: - Public methods
    
    @objc static func named(name: String!) -> UIFont? {
        guard  let info = Font.resource[name] as? [String: Any] else {
            return UIFont.systemFont(ofSize: UIFont.systemFontSize)
        }
        
        let name = info[keyName] as? String ?? Font.systemFontName
        let size = info[keySize] as? NSNumber
        let sizeValue = CGFloat(size ?? 0)

        if Font.systemFontName == name {
            let weight = self.weight(string: info[keyWeight] as? String)
            return UIFont.systemFont(ofSize: sizeValue, weight: weight)
        } else {
            return UIFont(name: name, size: sizeValue)
        }
    }
}
