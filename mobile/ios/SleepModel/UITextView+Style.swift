//
//  UITextView+Style.swift
//  Sense
//
//  Created by Jimmy Lu on 3/15/17.
//  Copyright © 2017 Hello. All rights reserved.
//

import Foundation

extension UITextView {
    
    @objc func applyStyle() {
        self.applyClassStyle(aClass: UITextView.self)
    }
    
    @objc func applyClassStyle(aClass: AnyClass) {
        guard let attributedText = self.attributedText else {
            self.textColor = SenseStyle.color(aClass: aClass, property: .textColor)
            self.font = SenseStyle.font(aClass: aClass, property: .textFont)
            return
        }
        
        guard let mutableText = attributedText.mutableCopy() as? NSMutableAttributedString else {
            return
        }
        
        let font = SenseStyle.font(aClass: aClass, property: .textFont)
        let color = SenseStyle.color(aClass: aClass, property: .textColor)
        let para = NSMutableParagraphStyle.senseStyle()
        let attributes: [NSAttributedString.Key: Any] = [NSAttributedString.Key.paragraphStyle : para,
                                         NSAttributedString.Key.font : font,
                                         NSAttributedString.Key.foregroundColor : color]
        
        mutableText.applyAttributes(attributes)
        self.attributedText = mutableText
        
        let linkColor = SenseStyle.color(aClass: aClass, property: .linkColor)
        self.linkTextAttributes = [NSAttributedString.Key.font : font,
                                   NSAttributedString.Key.foregroundColor : linkColor]
        
    }
}
